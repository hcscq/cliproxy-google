package helps

import (
	"context"
	"net/http"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// PublishUsage records a normal usage detail, or a content-safety block when
// the upstream payload carries one. A block stays HTTP 200: Home treats status
// 200 as a successful credential result, so the credential is not cooled down
// or quarantined. The usage row itself is failed, with the block code in
// fail.reason, so the block is visible without looking like an account ban.
func PublishUsage(ctx context.Context, reporter *UsageReporter, detail usage.Detail, raw []byte) {
	if reporter == nil {
		return
	}
	if block, ok := translatorcommon.ReadContentBlock(raw); ok {
		reporter.PublishContentBlock(ctx, detail, block)
		return
	}
	reporter.Publish(ctx, detail)
}

// PublishContentBlock records a request-scoped content-safety block.
func (r *UsageReporter) PublishContentBlock(ctx context.Context, detail usage.Detail, block translatorcommon.ContentBlock) {
	if r == nil {
		return
	}
	if !block.Active() {
		r.Publish(ctx, detail)
		return
	}
	log.WithContext(ctx).WithFields(log.Fields{
		"block_reason":  block.Reason,
		"finish_reason": block.FinishReason,
	}).Info("upstream content safety block")
	r.publishWithOutcome(ctx, detail, true, usage.Failure{
		StatusCode: http.StatusOK,
		Body:       block.UsageBody(),
		Reason:     block.UsageReason(),
	})
}

// ObserveContentBlock remembers a content-safety block seen in a stream.
// Later non-block chunks do not clear it, and an empty finishReason does not
// either. Publish then records the block instead of a normal success.
func (b *StreamUsageBuffer) ObserveContentBlock(raw []byte) {
	if b == nil || len(raw) == 0 {
		return
	}
	block, ok := translatorcommon.ReadContentBlock(raw)
	if !ok {
		block, ok = translatorcommon.ReadContentBlock(jsonPayload(raw))
	}
	if !ok {
		return
	}
	b.rememberContentBlock(block)
}

func (b *StreamUsageBuffer) rememberContentBlock(block translatorcommon.ContentBlock) {
	if block.Reason != "" {
		b.block.Reason = block.Reason
		if block.Message != "" {
			b.block.Message = block.Message
		}
	}
	if translatorcommon.IsSafetyFinishReason(block.FinishReason) {
		b.block.FinishReason = block.FinishReason
	}
	b.blocked = b.block.Active()
}
