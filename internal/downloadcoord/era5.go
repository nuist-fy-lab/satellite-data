package downloadcoord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/nuist-fy-lab/satellite-data/internal/downloader"
	"github.com/nuist-fy-lab/satellite-data/internal/model"
	"github.com/nuist-fy-lab/satellite-data/internal/provider/era5"
	"github.com/nuist-fy-lab/satellite-data/internal/state"
)

type CDSOrders interface {
	Process(context.Context, string) (era5.Process, error)
	Submit(context.Context, era5.Submission) (era5.Job, error)
	Job(context.Context, string) (era5.Job, error)
	Result(context.Context, string) (model.Asset, error)
}

func (c *Coordinator) advanceERA5(ctx context.Context, request state.DownloadRequest) error {
	if c.cds == nil {
		return errors.New("central CDS scheduling is unavailable")
	}
	part, partErr := c.store.ERA5OrderPart(ctx, request)
	if errors.Is(partErr, sql.ErrNoRows) {
		// Check the deployed transfer worker before creating a remote CDS job.
		providers, err := c.downloader.Providers(ctx)
		if err != nil {
			return err
		}
		ready := false
		for _, p := range providers {
			if p.SourceID == "era5" && p.Enabled {
				ready = true
			}
		}
		if !ready {
			return errors.New("ERA5 transfer worker is not enabled; centre will retry after deployment")
		}
		base, err := c.store.ERA5Inputs(ctx, request.ID)
		if err != nil {
			return err
		}
		process, err := c.cds.Process(ctx, request.CollectionID)
		if err != nil {
			return err
		}
		submission, end, err := era5.PlanOrderPart(request.CollectionID, request.Cursor, request.End, base, process)
		if err != nil {
			return err
		}
		if err := c.store.PrepareERA5Part(ctx, request, submission, end); err != nil {
			return err
		}
		part, partErr = c.store.ERA5OrderPart(ctx, request)
	}
	if partErr != nil {
		return partErr
	}
	if part.Stage == "submitting" && part.JobID == "" {
		return era5.ErrSubmissionUncertain
	}
	if part.JobID == "" {
		// A durable pre-POST checkpoint prevents duplicate CDS submissions after a crash.
		if err := c.store.SetERA5PartStage(ctx, request.ID, "submitting", ""); err != nil {
			return err
		}
		job, submitErr := c.cds.Submit(ctx, part.Submission)
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if job.ID != "" {
			if err := c.store.SetERA5PartStage(persistCtx, request.ID, "submitted", job.ID); err != nil {
				return fmt.Errorf("%w: job checkpoint failed", era5.ErrSubmissionUncertain)
			}
			part.JobID, part.Stage = job.ID, "submitted"
		}
		if submitErr != nil {
			if !errors.Is(submitErr, era5.ErrSubmissionUncertain) && job.ID == "" {
				if err := c.store.SetERA5PartStage(persistCtx, request.ID, "pending", ""); err != nil {
					return err
				}
			}
			return submitErr
		}
		if job.ID == "" {
			return era5.ErrSubmissionUncertain
		}
		return c.store.DeferDownloadOrder(persistCtx, request.ID, 15*time.Second)
	}
	job, err := c.cds.Job(ctx, part.JobID)
	if err != nil {
		return err
	}
	switch job.Status {
	case "successful":
	case "failed", "dismissed", "cancelled":
		return c.store.FailDownloadOrder(ctx, request.ID, fmt.Errorf("CDS job %s ended with status %s", part.JobID, job.Status))
	default:
		return c.store.DeferDownloadOrder(ctx, request.ID, 15*time.Second)
	}
	asset, err := c.cds.Result(ctx, part.JobID)
	if err != nil {
		return err
	}
	_, product, err := c.store.ProductSource(ctx, request.CollectionID)
	if err != nil {
		return err
	}
	// The downloader receives a stable centre-resolved URL, never an expiring signed URL.
	task, _, err := c.downloader.Enqueue(ctx, downloader.TaskSpec{
		SourceID: "era5", CollectionID: request.CollectionID, GranuleID: "era5:" + part.JobID,
		AssetID: asset.ID, Name: asset.Name, AuthRef: "cds", URL: "https://cds.climate.copernicus.eu/api/retrieve/v1/jobs/" + part.JobID + "/download",
		RelativeDir: path.Join(archiveRelativeDir("era5", product, part.Start), safeComponent(part.JobID)), Size: asset.Size, ETag: asset.ETag, MediaType: asset.MediaType,
	})
	if err != nil {
		return err
	}
	if asset.Size > 0 {
		if err := c.store.RecordERA5Result(ctx, request.ID, task.ID, part.JobID, request.CollectionID, asset.ID, asset.Name, asset.Size); err != nil {
			return fmt.Errorf("persist official CDS job result: %w", err)
		}
	}
	return c.store.AdvanceDownloadRequest(ctx, request.ID, part.End, 1, []state.DownloaderTaskRef{{ID: task.ID, Key: task.Key, Filename: task.Name}}, !part.End.Before(request.End))
}
