package reconcile

import (
	"context"
	"unsafe"
)

// Check validates the captured sources without reading result rows. Source
// lifecycle belongs to the caller; this operation borrows the prepared orders.
func (p *Relation) Check(ctx context.Context) error {
	orders, err := p.borrow()
	if err != nil {
		return err
	}
	defer orders[0].Close()
	defer orders[1].Close()
	return p.check(ctx)
}

// RetainedOrderHeapBytes covers the plan's own immutable order/candidate Go
// storage. Source descriptors and any detached values are charged by the
// owning server snapshot, independently of query temporary disk/mapped pages.
func (p *Relation) RetainedOrderHeapBytes() (int64, error) {
	orders, err := p.borrow()
	if err != nil {
		return 0, err
	}
	defer orders[0].Close()
	defer orders[1].Close()
	bytes := int64(8192)
	for _, order := range orders {
		weight, err := order.RetainedHeapBytes()
		if err != nil {
			return 0, err
		}
		bytes += weight
	}
	for _, candidates := range p.previews {
		bytes += int64(cap(candidates)) * int64(unsafe.Sizeof(relationPair{})) * 5 / 4
	}
	return bytes, nil
}
