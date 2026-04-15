package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	attachmentTTL     = 30 * time.Minute
	maxFileSize int64 = 10 * 1024 * 1024 // 10 MB
	maxJobSize  int64 = 30 * 1024 * 1024 // 30 MB
)

type RedisAttachmentStore struct {
	client *redis.Client
}

func NewRedisAttachmentStore(client *redis.Client) *RedisAttachmentStore {
	return &RedisAttachmentStore{client: client}
}

func (s *RedisAttachmentStore) attachmentKey(jobID string) string {
	return keyPrefix + "jobs:attachments:" + jobID
}

func (s *RedisAttachmentStore) Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error {
	var result []AttachmentReady
	var totalSize int64

	for _, p := range payloads {
		if p.Size > maxFileSize {
			slog.Warn("附件超過單檔上限，略過",
				"job_id", jobID, "filename", p.Filename, "size", p.Size, "limit", maxFileSize)
			continue
		}
		if totalSize+p.Size > maxJobSize {
			slog.Warn("附件超過工作總量上限，略過剩餘檔案",
				"job_id", jobID, "filename", p.Filename, "total_so_far", totalSize, "limit", maxJobSize)
			break
		}
		totalSize += p.Size
		result = append(result, AttachmentReady{
			Filename: p.Filename,
			Data:     p.Data,
			MimeType: p.MimeType,
		})
	}

	if result == nil {
		result = []AttachmentReady{}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.attachmentKey(jobID), data, attachmentTTL).Err()
}

func (s *RedisAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	key := s.attachmentKey(jobID)

	for {
		data, err := s.client.Get(ctx, key).Bytes()
		if err == nil {
			var result []AttachmentReady
			if err := json.Unmarshal(data, &result); err != nil {
				return nil, err
			}
			return result, nil
		}
		if err != redis.Nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *RedisAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	return s.client.Del(ctx, s.attachmentKey(jobID)).Err()
}
