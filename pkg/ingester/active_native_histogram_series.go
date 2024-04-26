// SPDX-License-Identifier: AGPL-3.0-only

package ingester

import (
	"context"
	"fmt"

	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/tenant"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb"

	"github.com/grafana/mimir/pkg/ingester/activeseries"
	"github.com/grafana/mimir/pkg/ingester/client"
	"github.com/grafana/mimir/pkg/mimirpb"
	"github.com/grafana/mimir/pkg/storage/sharding"
	"github.com/grafana/mimir/pkg/util/spanlogger"
)

const activeNativeHistogramSeriesChunkSize = 1 * 1024 * 1024

// ActiveNativeHistogramSeries implements the ActiveNativeHistogramSeries RPC.
// It returns a stream of active native histogram series and their active
// bucket counts for series that match the given matchers.
func (i *Ingester) ActiveNativeHistogramSeries(request *client.ActiveNativeHistogramSeriesRequest, stream client.Ingester_ActiveNativeHistogramSeriesServer) (err error) {
	defer func() { err = i.mapReadErrorToErrorWithStatus(err) }()
	if err := i.checkAvailableForRead(); err != nil {
		return err
	}
	if err := i.checkReadOverloaded(); err != nil {
		return err
	}

	spanlog, ctx := spanlogger.NewWithLogger(stream.Context(), i.logger, "Ingester.ActiveNativeHistogramSeries")
	defer spanlog.Finish()

	userID, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}

	matchers, err := client.FromLabelMatchers(request.GetMatchers())
	if err != nil {
		return fmt.Errorf("error parsing label matchers: %w", err)
	}

	// Enforce read consistency before getting TSDB (covers the case the tenant's data has not been ingested
	// in this ingester yet, but there's some to ingest in the backlog).
	if err := i.enforceReadConsistency(ctx, userID); err != nil {
		return err
	}

	db := i.getTSDB(userID)
	if db == nil {
		level.Debug(i.logger).Log("msg", "no TSDB for user", "userID", userID)
		return nil
	}

	idx, err := db.Head().Index()
	if err != nil {
		return fmt.Errorf("error getting index: %w", err)
	}

	nhPostings, err := listActiveNativeHistogramBuckets(ctx, db, idx, matchers)
	if err != nil {
		return fmt.Errorf("error listing active series: %w", err)
	}

	buf := labels.NewScratchBuilder(10)
	resp := &client.ActiveNativeHistogramSeriesResponse{}
	currentSize := 0
	for nhPostings.Next() {
		seriesRef, count := nhPostings.AtBucketCount()
		err = idx.Series(seriesRef, &buf, nil)
		if err != nil {
			return fmt.Errorf("error getting series: %w", err)
		}
		m := &mimirpb.Metric{Labels: mimirpb.FromLabelsToLabelAdapters(buf.Labels())}
		mSize := m.Size() + 8 // 8 bytes for the bucket count.
		if currentSize+mSize > activeNativeHistogramSeriesChunkSize {
			if err := client.SendActiveNativeHistogramSeriesResponse(stream, resp); err != nil {
				return fmt.Errorf("error sending response: %w", err)
			}
			resp = &client.ActiveNativeHistogramSeriesResponse{}
			currentSize = 0
		}
		resp.Series = append(resp.Series, &client.ActiveNativeHistogramSeriesInfo{Metric: m, BucketCount: int64(count)})
		currentSize += mSize
	}
	if err := nhPostings.Err(); err != nil {
		return fmt.Errorf("error iterating over series: %w", err)
	}

	if len(resp.Series) > 0 {
		if err := client.SendActiveNativeHistogramSeriesResponse(stream, resp); err != nil {
			return fmt.Errorf("error sending response: %w", err)
		}
	}

	return nil
}

// listActiveNativeHistogramBuckets returns an iterator over the active native histogram series matching the given matchers.
func listActiveNativeHistogramBuckets(ctx context.Context, db *userTSDB, idx tsdb.IndexReader, matchers []*labels.Matcher) (*activeseries.NativeHistogramPostings, error) {
	if db.activeSeries == nil {
		return nil, fmt.Errorf("active series tracker is not initialized")
	}

	shard, matchers, err := sharding.RemoveShardFromMatchers(matchers)
	if err != nil {
		return nil, fmt.Errorf("error removing shard matcher: %w", err)
	}

	postings, err := tsdb.PostingsForMatchers(ctx, idx, matchers...)
	if err != nil {
		return nil, fmt.Errorf("error getting postings: %w", err)
	}

	if shard != nil {
		postings = idx.ShardedPostings(postings, shard.ShardIndex, shard.ShardCount)
	}

	return activeseries.NewNativeHistogramPostings(db.activeSeries, postings), nil
}
