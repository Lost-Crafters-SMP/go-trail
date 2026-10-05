package trail

import "strings"

// processFresh keeps a concrete producer record until its payload is owned.
// Size/admission only borrow its temporary interface views; the FIFO conversion
// is the sole escaping box. No copying occurs for a rejected record. Sink still
// receives the existing public value types; queue entries do not grow.
func processFresh[R Record](p *AsyncProcessor, record R, own func(R) R) error {
	size, err := asyncRecordSize(record)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(record, size); err != nil {
		return err
	}
	p.enqueueLocked(asyncEntry{record: own(record), bytes: size})
	return nil
}

func ownFreshStart(r SpanStart) SpanStart {
	r.Name, r.Scope = strings.Clone(r.Name), strings.Clone(r.Scope)
	return r
}

func ownFreshUpdate(r SpanUpdate) SpanUpdate {
	r.Attributes = cloneAsyncAttributes(r.Attributes, true)
	if r.Status != nil {
		status := *r.Status
		status.Description = strings.Clone(status.Description)
		r.Status = &status
	}
	return r
}

func ownFreshEvent(r Event) Event {
	r.Name = strings.Clone(r.Name)
	r.Attributes = cloneAsyncAttributes(r.Attributes, true)
	return r
}
