package main

import (
	"fmt"
	"strconv"
	"testing"

	"go.lostcrafters.com/trail/internal/render"
)

func TestTopNBoundedSelection(t *testing.T) {
	const top = 10
	items := []render.Object{}
	for i := range 100000 {
		item := render.Object{"span_id": fmt.Sprintf("%016x", i+1), "start_elapsed_nanos": "0"}
		if i%2 == 0 {
			item["duration_nanos"] = strconv.Itoa(i)
		}
		items = retainResult(items, item, top, true, false)
		if len(items) > top {
			t.Fatal("top-N selection grew beyond bound")
		}
	}
	if len(items) != top {
		t.Fatal(len(items))
	}
	for _, item := range items {
		n, err := strconv.Atoi(item["duration_nanos"].(string))
		if err != nil || n < 99980 {
			t.Fatalf("wrong top result: %v", item)
		}
	}
}
