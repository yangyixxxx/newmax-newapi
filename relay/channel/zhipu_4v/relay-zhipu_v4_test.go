package zhipu_4v

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
)

func TestRequestOpenAI2ZhipuPreservesReasoningEffort(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model:           "glm-5.2",
		ReasoningEffort: "high",
		Messages: []dto.Message{{
			Role:    "user",
			Content: "hello",
		}},
	}

	got := requestOpenAI2Zhipu(request)

	if got.ReasoningEffort != request.ReasoningEffort {
		t.Fatalf("ReasoningEffort = %q, want %q", got.ReasoningEffort, request.ReasoningEffort)
	}
}
