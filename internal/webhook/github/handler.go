package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/jancernik/deeplo/internal/webhook"
)

// An http.Handler that accepts GitHub push webhooks on any path it is
// registered under. It validates the HMAC-SHA256 signature, deduplicates
// deliveries, parses the push payload, and dispatches asynchronously to onPush.
type Handler struct {
	appCtx context.Context
	secret []byte
	dedupe *DedupeCache
	onPush func(ctx context.Context, push webhook.PushEvent)
	logger *slog.Logger
}

func NewHandler(
	appCtx context.Context,
	secretFile string,
	dedupe *DedupeCache,
	onPush func(context.Context, webhook.PushEvent),
	logger *slog.Logger,
) (*Handler, error) {
	var secret []byte
	if secretFile != "" {
		raw, err := os.ReadFile(secretFile)
		if err != nil {
			return nil, fmt.Errorf("read webhook secret file: %w", err)
		}
		secret = []byte(strings.TrimSpace(string(raw)))
	}
	if dedupe == nil {
		dedupe = NewDedupeCache(defaultDedupeSize)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		appCtx: appCtx,
		secret: secret,
		dedupe: dedupe,
		onPush: onPush,
		logger: logger.With("component", "webhook"),
	}, nil
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MiB
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if len(handler.secret) > 0 {
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			handler.logger.Warn("webhook delivery missing signature")
			http.Error(w, "missing X-Hub-Signature-256", http.StatusUnauthorized)
			return
		}
		if !handler.validSignature(sig, body) {
			handler.logger.Warn("webhook delivery has invalid signature")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	eventType := r.Header.Get("X-GitHub-Event")
	if eventType != "push" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID != "" && handler.dedupe.Seen(deliveryID) {
		handler.logger.Info("duplicate webhook delivery ignored", "delivery_id", deliveryID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	payload, err := ParsePushPayload(body)
	if err != nil {
		handler.logger.Warn("failed to parse push payload", "err", err)
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}

	push := webhook.PushEvent{
		DeliveryID:   deliveryID,
		Branch:       payload.Branch(),
		CommitSha:    payload.After,
		RepoFullName: payload.Repository.FullName,
		ChangedFiles: payload.ChangedFiles(),
	}
	handler.logger.Info("push event received",
		"branch", push.Branch,
		"sha", push.CommitSha,
		"repo", push.RepoFullName,
		"changed_files", len(push.ChangedFiles),
		"delivery_id", deliveryID,
	)

	go handler.onPush(handler.appCtx, push)
	w.WriteHeader(http.StatusAccepted)
}

func (handler *Handler) validSignature(sigHeader string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	got, err := hex.DecodeString(sigHeader[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, handler.secret)
	mac.Write(body)
	want := mac.Sum(nil)
	return hmac.Equal(got, want)
}
