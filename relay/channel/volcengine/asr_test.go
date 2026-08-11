package volcengine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestASRAudioFormat(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
		wantFormat  string
		wantCodec   string
		wantErr     bool
	}{
		{name: "wav ext", filename: "a.WAV", wantFormat: "wav", wantCodec: "raw"},
		{name: "mp3 ext", filename: "a.mp3", wantFormat: "mp3", wantCodec: "raw"},
		{name: "opus ext", filename: "a.opus", wantFormat: "ogg", wantCodec: "opus"},
		{name: "pcm ext", filename: "a.pcm", wantFormat: "pcm", wantCodec: "raw"},
		{name: "content type fallback", filename: "noext", contentType: "audio/mpeg; rate=16000", wantFormat: "mp3", wantCodec: "raw"},
		{name: "unsupported", filename: "a.m4a", contentType: "audio/mp4", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, codec, err := asrAudioFormat(tt.filename, tt.contentType)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFormat, format)
			assert.Equal(t, tt.wantCodec, codec)
		})
	}
}

func newTestWSPair(t *testing.T, serverHandler func(*websocket.Conn)) *websocket.Conn {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		serverHandler(serverConn)
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { clientConn.Close() })
	return clientConn
}

func TestSendASRAudioFraming(t *testing.T) {
	audio := make([]byte, asrAudioChunkSize+100)
	for i := range audio {
		audio[i] = byte(i % 251)
	}

	frames := make(chan *Message, 8)
	done := make(chan struct{})
	clientConn := newTestWSPair(t, func(serverConn *websocket.Conn) {
		defer close(done)
		defer serverConn.Close()
		for i := 0; i < 2; i++ {
			msg, err := ReceiveMessage(serverConn)
			if err != nil {
				t.Errorf("server receive failed: %v", err)
				return
			}
			frames <- msg
		}
	})

	require.NoError(t, sendASRAudio(clientConn, audio))
	<-done
	close(frames)

	var received []*Message
	for msg := range frames {
		received = append(received, msg)
	}
	require.Len(t, received, 2)

	first, last := received[0], received[1]
	assert.Equal(t, MsgTypeAudioOnlyClient, first.MsgType)
	assert.Equal(t, MsgTypeFlagPositiveSeq, first.MsgTypeFlag)
	assert.Equal(t, int32(2), first.Sequence)
	assert.Equal(t, CompressionGzip, first.Compression)

	assert.Equal(t, MsgTypeFlagNegativeSeq, last.MsgTypeFlag)
	assert.Equal(t, int32(-3), last.Sequence)
	assert.True(t, asrIsLastMessage(last))
	assert.False(t, asrIsLastMessage(first))

	firstBody, err := asrMessagePayload(first)
	require.NoError(t, err)
	lastBody, err := asrMessagePayload(last)
	require.NoError(t, err)
	assert.Equal(t, audio, append(firstBody, lastBody...))
	assert.Len(t, firstBody, asrAudioChunkSize)
}

func TestHandleASRWebSocketResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	finalResult := asrResponsePayload{
		AudioInfo: asrAudioInfo{Duration: 3696},
		Result: asrResult{
			Text: "今天天气不错。",
			Utterances: []asrUtterance{
				{Text: "今天天气不错。", StartTime: 0, EndTime: 1705, Definite: true},
			},
		},
	}

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "volc.seedasr.sauc.duration", r.Header.Get("X-Api-Resource-Id"))
		assert.Equal(t, "test-app", r.Header.Get("X-Api-App-Key"))
		assert.Equal(t, "test-token", r.Header.Get("X-Api-Access-Key"))
		assert.NotEmpty(t, r.Header.Get("X-Api-Request-Id"))

		serverConn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer serverConn.Close()

		fullReq, err := ReceiveMessage(serverConn)
		require.NoError(t, err)
		require.Equal(t, MsgTypeFullClientRequest, fullReq.MsgType)
		reqBody, err := asrMessagePayload(fullReq)
		require.NoError(t, err)
		var payload asrRequestPayload
		require.NoError(t, common.Unmarshal(reqBody, &payload))
		assert.Equal(t, asrUpstreamModel, payload.Request.ModelName)
		assert.Equal(t, "wav", payload.Audio.Format)

		for {
			msg, err := ReceiveMessage(serverConn)
			require.NoError(t, err)
			require.Equal(t, MsgTypeAudioOnlyClient, msg.MsgType)
			if asrIsLastMessage(msg) {
				break
			}
		}

		respBody, err := common.Marshal(finalResult)
		require.NoError(t, err)
		compressed, err := gzipCompress(respBody)
		require.NoError(t, err)
		respMsg, err := NewMessage(MsgTypeFullServerResponse, MsgTypeFlagNegativeSeq)
		require.NoError(t, err)
		respMsg.Compression = CompressionGzip
		respMsg.Sequence = -3
		respMsg.Payload = compressed
		frame, err := respMsg.Marshal()
		require.NoError(t, err)
		require.NoError(t, serverConn.WriteMessage(websocket.BinaryMessage, frame))
	}))
	defer srv.Close()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:            "test-app|test-token",
			UpstreamModelName: "doubao-seed-asr",
		},
	}
	info.SetEstimatePromptTokens(100)

	asrReq := &asrRelayRequest{
		Audio:          []byte("fake-wav-bytes"),
		Format:         "wav",
		Codec:          "raw",
		ResourceID:     "volc.seedasr.sauc.duration",
		ResponseFormat: "json",
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	usage, apiErr := handleASRWebSocketResponse(c, wsURL, asrReq, info)
	require.Nil(t, apiErr)

	var audioResp dto.AudioResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &audioResp))
	assert.Equal(t, "今天天气不错。", audioResp.Text)

	dtoUsage, ok := usage.(*dto.Usage)
	require.True(t, ok)
	assert.Equal(t, 100, dtoUsage.PromptTokens)
	assert.Greater(t, dtoUsage.CompletionTokens, 0)
}

func TestASRFormatSubtitles(t *testing.T) {
	resp := asrResponsePayload{
		AudioInfo: asrAudioInfo{Duration: 3696},
		Result: asrResult{
			Text: "第一句。第二句。",
			Utterances: []asrUtterance{
				{Text: "第一句。", StartTime: 0, EndTime: 1705},
				{Text: "第二句。", StartTime: 1800, EndTime: 3696},
			},
		},
	}

	srt := asrFormatSubtitles(resp, "srt")
	assert.Equal(t, "1\n00:00:00,000 --> 00:00:01,705\n第一句。\n\n2\n00:00:01,800 --> 00:00:03,696\n第二句。\n", srt)

	vtt := asrFormatSubtitles(resp, "vtt")
	assert.Equal(t, "WEBVTT\n\n00:00:00.000 --> 00:00:01.705\n第一句。\n\n00:00:01.800 --> 00:00:03.696\n第二句。\n", vtt)

	noUtterances := asrResponsePayload{
		AudioInfo: asrAudioInfo{Duration: 2000},
		Result:    asrResult{Text: "整段。"},
	}
	assert.Equal(t, "1\n00:00:00,000 --> 00:00:02,000\n整段。\n", asrFormatSubtitles(noUtterances, "srt"))
}
