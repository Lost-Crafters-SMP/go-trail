package trail

import (
	"context"
	"errors"
)

// backgroundError never runs on the writer. Callback reentry cannot block it.
func (ps *providerState) backgroundError(err error) {
	ps.mu.Lock()
	ps.latchError(err)
	ps.unlockAndReport()
}

func (ps *providerState) flushAsync(ctx context.Context, ap *AsyncProcessor) error {
	if err := ap.acquireControl(ctx); err != nil {
		ps.mu.Lock()
		defer ps.unlockAndReport()
		if ps.stage == stageClosed {
			return ps.termErr
		}
		return err
	}
	ps.mu.Lock()
	if ps.stage == stageClosed || ctx.Err() != nil {
		ap.control <- struct{}{}
		err := ctx.Err()
		if ps.stage == stageClosed {
			err = ps.termErr
		}
		ps.unlockAndReport()
		return err
	}
	summary := ps.lossSnapshot()
	b := ap.checkpoint(ctx, &summary, false)
	ps.unlockAndReport()
	err := waitAsyncBarrier(ctx, b)
	ps.mu.Lock()
	defer ps.unlockAndReport()
	if err != nil && !isCancellation(err) {
		ap.mu.Lock()
		pipelineErr := ap.err
		ap.mu.Unlock()
		if pipelineErr != nil {
			ps.latchError(pipelineErr)
		} else {
			ps.latchError(err)
		}
	}
	var loss error
	if summary.RejectedStarts != 0 {
		loss = &IncompleteError{RejectedStarts: summary.RejectedStarts}
	}
	return errors.Join(err, ps.stickyErr, loss)
}

func (ps *providerState) shutdownAsync(ctx context.Context, ap *AsyncProcessor) error {
	ps.mu.Lock()
	if ps.stage == stageClosed {
		err := ps.termErr
		ps.unlockAndReport()
		<-ap.done
		return err
	}
	ps.stage = stageClosing
	ps.unlockAndReport()
	ap.stopAdmission()
	if err := ap.acquireControl(ctx); err != nil {
		ap.mu.Lock()
		closed, terminal := ap.closed, ap.termErr
		ap.mu.Unlock()
		if closed {
			return ps.finishAsyncShutdown(ap, terminal)
		}
		ps.mu.Lock()
		defer ps.unlockAndReport()
		return errors.Join(err, ps.stickyErr)
	}
	ps.mu.Lock()
	if err := ctx.Err(); err != nil {
		ap.control <- struct{}{}
		ps.unlockAndReport()
		return err
	}
	var summary *LossSummary
	if !ps.finalSummaryAttempted {
		current := ps.lossSnapshot()
		summary = &current
		ps.finalSummaryAttempted = true
	}
	b := ap.checkpoint(ctx, summary, true)
	ps.unlockAndReport()
	err := waitAsyncBarrier(ctx, b)
	ap.mu.Lock()
	closed, terminal := ap.closed, ap.termErr
	ap.mu.Unlock()
	if closed {
		return ps.finishAsyncShutdown(ap, terminal)
	}
	ps.mu.Lock()
	defer ps.unlockAndReport()
	return errors.Join(err, ps.stickyErr)
}

func (ps *providerState) finishAsyncShutdown(ap *AsyncProcessor, err error) error {
	<-ap.done
	ps.mu.Lock()
	defer ps.unlockAndReport()
	if ps.stage == stageClosed {
		return ps.termErr
	}
	if err != nil {
		ps.notifyCleanupError(err)
	}
	ps.stage = stageClosed
	ps.termErr = errors.Join(err, ps.stickyErr, ps.lossError())
	return ps.termErr
}
