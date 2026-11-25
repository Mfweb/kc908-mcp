// Copyright (c) 2026 Mfweb
// SPDX-License-Identifier: MIT

package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// JobStatus snapshots the state and progress of a background image upload.
type JobStatus struct {
	TransferID  string     `json:"transfer_id"`
	State       string     `json:"state"`
	Transferred uint32     `json:"transferred_bytes"`
	Total       uint32     `json:"total_bytes"`
	Progress    uint32     `json:"progress"`
	Result      *ImageInfo `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
}

type imageJob struct {
	status JobStatus
	cancel context.CancelFunc
	done   chan struct{}
}

// StartUpload starts a background image upload and returns its initial status.
func (c *Client) StartUpload(data []byte) (JobStatus, error) {
	if len(data) > imageMax || detectImageFormat(data) == 0 {
		return JobStatus{}, fmt.Errorf("expected PNG JPEG or BMP up to 16 MiB")
	}
	if !c.mu.TryLock() {
		return JobStatus{}, fmt.Errorf("image transfer busy; query image_transfer_status")
	}

	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		c.mu.Unlock()
		return JobStatus{}, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &imageJob{
		status: JobStatus{
			TransferID: hex.EncodeToString(token[:]),
			State:      "uploading",
			Total:      uint32(len(data)),
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}

	c.jobsMu.Lock()
	c.job = job
	initial := job.status
	c.jobsMu.Unlock()

	go func() {
		defer cancel()

		info, err := c.uploadLocked(ctx, data, func(transferred uint32) {
			c.jobsMu.Lock()
			job.status.Transferred = transferred
			job.status.Progress = transferred * 99 / job.status.Total
			c.jobsMu.Unlock()
		})

		// uploadLocked closes the device session before making the terminal
		// state visible so cancellation guarantees the SD lease is released.
		c.jobsMu.Lock()
		if err == nil {
			job.status.State = "completed"
			job.status.Progress = 100
			job.status.Result = &info
		} else {
			job.status.State = "failed"
			if ctx.Err() != nil {
				job.status.State = "cancelled"
			}
			job.status.Error = err.Error()
		}
		c.mu.Unlock()
		close(job.done)
		c.jobsMu.Unlock()
	}()

	return initial, nil
}

// TransferStatus reads the latest background upload job status and optionally
// cancels an active upload.
func (c *Client) TransferStatus(transferID string, cancel bool) (JobStatus, error) {
	c.jobsMu.Lock()
	defer c.jobsMu.Unlock()

	if c.job == nil || transferID == "" || transferID != c.job.status.TransferID {
		return JobStatus{}, fmt.Errorf("unknown or expired image transfer_id")
	}

	job := c.job
	if cancel && job.status.State == "uploading" {
		job.status.State = "cancelling"
		job.cancel()
	}
	return job.status, nil
}

// Shutdown cancels any active background upload and waits for cleanup to finish.
func (c *Client) Shutdown() {
	c.jobsMu.Lock()
	job := c.job
	if job != nil {
		job.cancel()
	}
	c.jobsMu.Unlock()

	if job != nil {
		<-job.done
	}
}
