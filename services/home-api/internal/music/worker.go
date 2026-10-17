package music

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"homenode/services/home-api/internal/store"
)

func Run(ctx context.Context, database *store.Store, client *Client, musicRoot string, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := processOne(ctx, database, client, musicRoot, logger); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("music queue check failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func processOne(ctx context.Context, database *store.Store, client *Client, musicRoot string, logger *slog.Logger) error {
	if ok, _ := client.Verified(); !ok {
		return nil
	}
	cooldown, err := database.MusicCooldown(ctx)
	if err != nil {
		return err
	}
	if time.Now().Before(cooldown) {
		return nil
	}
	if err := database.ResumeMusicVerificationJobs(ctx); err != nil {
		return err
	}
	job, ok, err := database.ClaimMusicJob(ctx)
	if err != nil || !ok {
		return err
	}
	path, err := client.Download(ctx, job, musicRoot)
	if err == nil {
		logger.Info("music track downloaded", "job_id", job.ID)
		return database.UpdateMusicJob(ctx, job.ID, "done", "", "", path)
	}
	if errors.Is(err, context.Canceled) {
		// The next start resets interrupted running jobs to queued.
		return err
	}
	if errors.Is(err, ErrVerificationRequired) {
		return database.UpdateMusicJob(ctx, job.ID, "verification", err.Error(), "", "")
	}
	var retry *RetryError
	if errors.As(err, &retry) {
		until := time.Now().Add(max(retry.Delay, time.Minute))
		if err := database.SetMusicCooldown(ctx, until); err != nil {
			return err
		}
		when := until.UTC().Format(time.RFC3339)
		return database.UpdateMusicJob(ctx, job.ID, "retry", err.Error(), when, "")
	}
	if job.Attempts < 5 {
		backoff := min(time.Duration(1<<min(job.Attempts, 6))*time.Minute, 2*time.Hour)
		when := time.Now().Add(backoff).UTC().Format(time.RFC3339)
		logger.Warn("music download will retry", "job_id", job.ID, "error", err)
		return database.UpdateMusicJob(ctx, job.ID, "retry", err.Error(), when, "")
	}
	logger.Error("music download failed", "job_id", job.ID, "error", err)
	return database.UpdateMusicJob(ctx, job.ID, "failed", err.Error(), "", "")
}
