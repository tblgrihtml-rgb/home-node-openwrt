package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMusicQueuePersistsAndRecoversRunningJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job, err := db.AddMusicJob(ctx, 123, "Track", "Artist", "Album")
	if err != nil || job.Status != "queued" {
		t.Fatalf("add: %+v, %v", job, err)
	}
	duplicate, err := db.AddMusicJob(ctx, 123, "Other", "Other", "Other")
	if err != nil || duplicate.ID != job.ID || duplicate.Title != "Track" {
		t.Fatalf("duplicate: %+v, %v", duplicate, err)
	}
	claimed, ok, err := db.ClaimMusicJob(ctx)
	if err != nil || !ok || claimed.Attempts != 1 || claimed.Status != "running" {
		t.Fatalf("claim: %+v, %t, %v", claimed, ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claimed, ok, err = db.ClaimMusicJob(ctx)
	if err != nil || !ok || claimed.Attempts != 2 {
		t.Fatalf("recovery: %+v, %t, %v", claimed, ok, err)
	}
	if err := db.UpdateMusicJob(ctx, job.ID, "done", "", "", "music/Artist/Album/Track.flac"); err != nil {
		t.Fatal(err)
	}
	jobs, err := db.ListMusicJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Status != "done" {
		t.Fatalf("list: %+v, %v", jobs, err)
	}
}

func TestMusicQueueRetryAndRemove(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	job, err := db.AddMusicJob(ctx, 456, "Title", "Artist", "Album")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := db.ClaimMusicJob(ctx)
	if err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("claim: %+v, %t, %v", claimed, ok, err)
	}
	if removed, err := db.RemoveMusicJob(ctx, job.ID); err != nil || removed {
		t.Fatalf("running job must not be removed: %t, %v", removed, err)
	}
	if err := db.UpdateMusicJob(ctx, job.ID, "failed", "network", "", ""); err != nil {
		t.Fatal(err)
	}
	if retried, err := db.RetryMusicJob(ctx, job.ID); err != nil || !retried {
		t.Fatalf("retry: %t, %v", retried, err)
	}
	if removed, err := db.RemoveMusicJob(ctx, job.ID); err != nil || !removed {
		t.Fatalf("remove: %t, %v", removed, err)
	}
}

func TestMusicCooldownPersists(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := db.SetMusicCooldown(ctx, until); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMusicCooldown(ctx, until.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := db.MusicCooldown(ctx)
	if err != nil || !got.Equal(until) {
		t.Fatalf("cooldown: %v, %v", got, err)
	}
}
