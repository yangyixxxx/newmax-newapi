package volcengine

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// 大模型流式语音识别（sauc bigmodel）：https://docs.volcengine.com/docs/6561/1354869
// 网关侧走「流式输入模式」（bigmodel_nostream）：音频整体分包送完后取最终识别结果，
// 对外表现为 OpenAI 兼容的 /v1/audio/transcriptions。
const (
	asrDefaultEndpoint = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_nostream"
	asrUpstreamModel   = "bigmodel"

	asrAudioChunkSize = 64 * 1024
	asrWriteTimeout   = 15 * time.Second
	asrReadTimeout    = 120 * time.Second
)

// 资源 ID 决定上游计费轨道（1.0/2.0 × 小时版/并发版），随对外模型名切换。
// 对外只卖 2.0（doubao-seed-asr，¥1/h，比 1.0 便宜 4.5 倍且并发配额更高）；
// 1.0/并发版映射保留是为了已配置渠道的向后兼容，未收录模型一律回落 2.0 小时版。
var asrResourceIDs = map[string]string{
	"doubao-asr":                 "volc.bigasr.sauc.duration",
	"doubao-asr-concurrent":      "volc.bigasr.sauc.concurrent",
	"doubao-seed-asr":            "volc.seedasr.sauc.duration",
	"doubao-seed-asr-concurrent": "volc.seedasr.sauc.concurrent",
	asrUpstreamModel:             "volc.seedasr.sauc.duration",
}

type asrRelayRequest struct {
	Audio          []byte
	Format         string
	Codec          string
	Language       string
	ResourceID     string
	ResponseFormat string
}

type asrRequestPayload struct {
	User    asrUserPayload    `json:"user"`
	Audio   asrAudioPayload   `json:"audio"`
	Request asrRequestOptions `json:"request"`
}

type asrUserPayload struct {
	UID string `json:"uid"`
}

type asrAudioPayload struct {
	Format   string `json:"format"`
	Codec    string `json:"codec,omitempty"`
	Rate     int    `json:"rate,omitempty"`
	Bits     int    `json:"bits,omitempty"`
	Channel  int    `json:"channel,omitempty"`
	Language string `json:"language,omitempty"`
}

type asrRequestOptions struct {
	ModelName      string `json:"model_name"`
	EnableITN      bool   `json:"enable_itn"`
	EnablePunc     bool   `json:"enable_punc"`
	ShowUtterances bool   `json:"show_utterances"`
	ResultType     string `json:"result_type"`
}

type asrResponsePayload struct {
	AudioInfo asrAudioInfo `json:"audio_info"`
	Result    asrResult    `json:"result"`
}

type asrAudioInfo struct {
	Duration int `json:"duration"`
}

type asrResult struct {
	Text       string         `json:"text"`
	Utterances []asrUtterance `json:"utterances"`
}

type asrUtterance struct {
	Text      string `json:"text"`
	StartTime int    `json:"start_time"`
	EndTime   int    `json:"end_time"`
	Definite  bool   `json:"definite"`
}

var asrExtFormats = map[string]struct{ format, codec string }{
	".wav":  {"wav", "raw"},
	".mp3":  {"mp3", "raw"},
	".ogg":  {"ogg", "opus"},
	".opus": {"ogg", "opus"},
	".pcm":  {"pcm", "raw"},
	".raw":  {"pcm", "raw"},
}

var asrContentTypeFormats = map[string]struct{ format, codec string }{
	"audio/wav":  {"wav", "raw"},
	"audio/wave": {"wav", "raw"},
	"audio/mpeg": {"mp3", "raw"},
	"audio/mp3":  {"mp3", "raw"},
	"audio/ogg":  {"ogg", "opus"},
	"audio/opus": {"ogg", "opus"},
}

// OpenAI 的 language 是 ISO-639-1 短码，上游要区域全码；未收录的原样透传。
var asrLanguageAliases = map[string]string{
	"zh": "zh-CN",
	"en": "en-US",
	"ja": "ja-JP",
	"ko": "ko-KR",
	"es": "es-MX",
	"fr": "fr-FR",
	"ru": "ru-RU",
	"pt": "pt-BR",
	"de": "de-DE",
	"it": "it-IT",
	"id": "id-ID",
	"th": "th-TH",
	"vi": "vi-VN",
	"ms": "ms-MY",
	"ar": "ar-SA",
}

func buildASRRelayRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (*asrRelayRequest, error) {
	formData, err := common.ParseMultipartFormReusable(c)
	if err != nil {
		return nil, fmt.Errorf("error parsing multipart form: %w", err)
	}

	fileHeaders := formData.File["file"]
	if len(fileHeaders) == 0 {
		return nil, errors.New("file is required")
	}
	fileHeader := fileHeaders[0]

	format, codec, err := asrAudioFormat(fileHeader.Filename, fileHeader.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}

	file, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("error opening audio file: %w", err)
	}
	defer file.Close()

	audioData, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("error reading audio file: %w", err)
	}
	if len(audioData) == 0 {
		return nil, errors.New("audio file is empty")
	}

	language := ""
	if values := formData.Value["language"]; len(values) > 0 {
		language = values[0]
	}
	if alias, ok := asrLanguageAliases[strings.ToLower(language)]; ok {
		language = alias
	}

	responseFormat := request.ResponseFormat
	if values := formData.Value["response_format"]; len(values) > 0 && values[0] != "" {
		responseFormat = values[0]
	}
	if responseFormat == "" {
		responseFormat = "json"
	}

	resourceID, ok := asrResourceIDs[info.UpstreamModelName]
	if !ok {
		resourceID = asrResourceIDs[asrUpstreamModel]
	}

	return &asrRelayRequest{
		Audio:          audioData,
		Format:         format,
		Codec:          codec,
		Language:       language,
		ResourceID:     resourceID,
		ResponseFormat: responseFormat,
	}, nil
}

func asrAudioFormat(filename, contentType string) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if entry, ok := asrExtFormats[ext]; ok {
		return entry.format, entry.codec, nil
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if entry, ok := asrContentTypeFormats[mediaType]; ok {
		return entry.format, entry.codec, nil
	}
	return "", "", fmt.Errorf("unsupported audio format %q, supported: wav, mp3, ogg/opus, pcm", ext)
}

func handleASRWebSocketResponse(c *gin.Context, requestURL string, asrReq *asrRelayRequest, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	header := http.Header{}
	if strings.Contains(info.ApiKey, "|") {
		appID, token, parseErr := parseVolcengineAuth(info.ApiKey)
		if parseErr != nil {
			return nil, types.NewErrorWithStatusCode(parseErr, types.ErrorCodeChannelInvalidKey, http.StatusUnauthorized)
		}
		header.Set("X-Api-App-Key", appID)
		header.Set("X-Api-Access-Key", token)
	} else {
		header.Set("X-Api-Key", info.ApiKey)
	}
	header.Set("X-Api-Resource-Id", asrReq.ResourceID)
	header.Set("X-Api-Request-Id", generateRequestID())

	conn, resp, dialErr := websocket.DefaultDialer.DialContext(c.Request.Context(), requestURL, header)
	if dialErr != nil {
		if resp != nil {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("failed to connect to websocket: %w, status: %d, logid: %s", dialErr, resp.StatusCode, resp.Header.Get("X-Tt-Logid")),
				types.ErrorCodeBadResponseStatusCode,
				http.StatusBadGateway,
			)
		}
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("failed to connect to websocket: %w", dialErr),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusBadGateway,
		)
	}
	defer conn.Close()

	payload, marshalErr := common.Marshal(buildASRPayload(asrReq, info))
	if marshalErr != nil {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("failed to marshal request: %w", marshalErr),
			types.ErrorCodeBadRequestBody,
			http.StatusInternalServerError,
		)
	}

	if sendErr := sendASRFullRequest(conn, payload); sendErr != nil {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("failed to send request: %w", sendErr),
			types.ErrorCodeBadRequestBody,
			http.StatusInternalServerError,
		)
	}

	writeErrCh := make(chan error, 1)
	go func() {
		writeErrCh <- sendASRAudio(conn, asrReq.Audio)
	}()

	var finalResp asrResponsePayload
	for {
		_ = conn.SetReadDeadline(time.Now().Add(asrReadTimeout))
		msg, recvErr := ReceiveMessage(conn)
		if recvErr != nil {
			select {
			case writeErr := <-writeErrCh:
				if writeErr != nil {
					recvErr = fmt.Errorf("%w (write error: %v)", recvErr, writeErr)
				}
			default:
			}
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("failed to receive message: %w", recvErr),
				types.ErrorCodeBadResponse,
				http.StatusBadGateway,
			)
		}

		body, payloadErr := asrMessagePayload(msg)
		if payloadErr != nil {
			return nil, types.NewErrorWithStatusCode(payloadErr, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}

		switch msg.MsgType {
		case MsgTypeError:
			statusCode := http.StatusBadGateway
			// 45xxxxxx 段是请求侧错误（参数无效/空音频/格式不对等）
			if msg.ErrorCode >= 45000000 && msg.ErrorCode < 46000000 {
				statusCode = http.StatusBadRequest
			}
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("volcengine asr error: code=%d, %s", msg.ErrorCode, string(body)),
				types.ErrorCodeBadResponse,
				statusCode,
			)
		case MsgTypeFullServerResponse:
			if len(body) > 0 {
				if unmarshalErr := common.Unmarshal(body, &finalResp); unmarshalErr != nil {
					return nil, types.NewErrorWithStatusCode(
						fmt.Errorf("failed to parse asr response: %w", unmarshalErr),
						types.ErrorCodeBadResponseBody,
						http.StatusBadGateway,
					)
				}
			}
			if asrIsLastMessage(msg) {
				return writeASRResponse(c, asrReq, info, finalResp)
			}
		default:
		}
	}
}

func buildASRPayload(asrReq *asrRelayRequest, info *relaycommon.RelayInfo) asrRequestPayload {
	audio := asrAudioPayload{
		Format:   asrReq.Format,
		Codec:    asrReq.Codec,
		Language: asrReq.Language,
	}
	if asrReq.Format == "pcm" {
		audio.Rate = 16000
		audio.Bits = 16
		audio.Channel = 1
	}
	return asrRequestPayload{
		User:  asrUserPayload{UID: fmt.Sprintf("gateway_user_%d", info.UserId)},
		Audio: audio,
		Request: asrRequestOptions{
			ModelName:      asrUpstreamModel,
			EnableITN:      true,
			EnablePunc:     true,
			ShowUtterances: true,
			ResultType:     "full",
		},
	}
}

func sendASRFullRequest(conn *websocket.Conn, payload []byte) error {
	compressed, err := gzipCompress(payload)
	if err != nil {
		return err
	}
	msg, err := NewMessage(MsgTypeFullClientRequest, MsgTypeFlagPositiveSeq)
	if err != nil {
		return err
	}
	msg.Compression = CompressionGzip
	msg.Sequence = 1
	msg.Payload = compressed
	return writeASRMessage(conn, msg)
}

func sendASRAudio(conn *websocket.Conn, audio []byte) error {
	seq := int32(1)
	for offset := 0; offset < len(audio); offset += asrAudioChunkSize {
		end := offset + asrAudioChunkSize
		if end > len(audio) {
			end = len(audio)
		}
		compressed, err := gzipCompress(audio[offset:end])
		if err != nil {
			return err
		}

		seq++
		flag := MsgTypeFlagPositiveSeq
		sequence := seq
		if end == len(audio) {
			flag = MsgTypeFlagNegativeSeq
			sequence = -seq
		}

		msg, err := NewMessage(MsgTypeAudioOnlyClient, flag)
		if err != nil {
			return err
		}
		msg.Serialization = SerializationNone
		msg.Compression = CompressionGzip
		msg.Sequence = sequence
		msg.Payload = compressed
		if err := writeASRMessage(conn, msg); err != nil {
			return err
		}
	}
	return nil
}

func writeASRMessage(conn *websocket.Conn, msg *Message) error {
	frame, err := msg.Marshal()
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(asrWriteTimeout))
	return conn.WriteMessage(websocket.BinaryMessage, frame)
}

func asrMessagePayload(msg *Message) ([]byte, error) {
	if msg.Compression != CompressionGzip || len(msg.Payload) == 0 {
		return msg.Payload, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(msg.Payload))
	if err != nil {
		return nil, fmt.Errorf("failed to decompress asr payload: %w", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress asr payload: %w", err)
	}
	return body, nil
}

func asrIsLastMessage(msg *Message) bool {
	return msg.MsgTypeFlag == MsgTypeFlagNegativeSeq || msg.MsgTypeFlag == MsgTypeFlagBits(0b10) || msg.Sequence < 0
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeASRResponse(c *gin.Context, asrReq *asrRelayRequest, info *relaycommon.RelayInfo, resp asrResponsePayload) (any, *types.NewAPIError) {
	var body []byte
	var contentType string

	switch asrReq.ResponseFormat {
	case "verbose_json":
		verbose := dto.WhisperVerboseJSONResponse{
			Task:     "transcribe",
			Language: asrReq.Language,
			Duration: float64(resp.AudioInfo.Duration) / 1000.0,
			Text:     resp.Result.Text,
		}
		for i, utterance := range resp.Result.Utterances {
			verbose.Segments = append(verbose.Segments, dto.Segment{
				Id:    i,
				Start: float64(utterance.StartTime) / 1000.0,
				End:   float64(utterance.EndTime) / 1000.0,
				Text:  utterance.Text,
			})
		}
		marshaled, err := common.Marshal(verbose)
		if err != nil {
			return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		body, contentType = marshaled, "application/json"
	case "text":
		body, contentType = []byte(resp.Result.Text), "text/plain; charset=utf-8"
	case "srt":
		body, contentType = []byte(asrFormatSubtitles(resp, "srt")), "text/plain; charset=utf-8"
	case "vtt":
		body, contentType = []byte(asrFormatSubtitles(resp, "vtt")), "text/plain; charset=utf-8"
	default:
		marshaled, err := common.Marshal(dto.AudioResponse{Text: resp.Result.Text})
		if err != nil {
			return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		body, contentType = marshaled, "application/json"
	}

	c.Data(http.StatusOK, contentType, body)
	usage := service.ResponseText2Usage(c, resp.Result.Text, info.UpstreamModelName, info.GetEstimatePromptTokens())
	return usage, nil
}

func asrFormatSubtitles(resp asrResponsePayload, format string) string {
	var builder strings.Builder
	if format == "vtt" {
		builder.WriteString("WEBVTT\n\n")
	}
	utterances := resp.Result.Utterances
	if len(utterances) == 0 && resp.Result.Text != "" {
		utterances = []asrUtterance{{Text: resp.Result.Text, StartTime: 0, EndTime: resp.AudioInfo.Duration}}
	}
	for i, utterance := range utterances {
		if format == "srt" {
			builder.WriteString(fmt.Sprintf("%d\n", i+1))
			builder.WriteString(fmt.Sprintf("%s --> %s\n", asrSubtitleTime(utterance.StartTime, ","), asrSubtitleTime(utterance.EndTime, ",")))
		} else {
			builder.WriteString(fmt.Sprintf("%s --> %s\n", asrSubtitleTime(utterance.StartTime, "."), asrSubtitleTime(utterance.EndTime, ".")))
		}
		builder.WriteString(utterance.Text)
		builder.WriteString("\n\n")
	}
	return strings.TrimRight(builder.String(), "\n") + "\n"
}

func asrSubtitleTime(milliseconds int, millisSeparator string) string {
	if milliseconds < 0 {
		milliseconds = 0
	}
	hours := milliseconds / 3600000
	minutes := (milliseconds % 3600000) / 60000
	seconds := (milliseconds % 60000) / 1000
	millis := milliseconds % 1000
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", hours, minutes, seconds, millisSeparator, millis)
}
