package dynamodb

import (
	"cmp"
	"container/heap"
)

// Discovery merges elapsed timers with priority-ordered ready partitions. Retain only the
// best candidates rather than allocating and sorting a ready backlog.
// These are hints: claimCandidate still checks current eligibility and revision.
const candidateLimit = 128

func compareCandidates(left, right candidate) int {
	return cmp.Or(cmp.Compare(right.priority, left.priority), cmp.Compare(left.due, right.due),
		cmp.Compare(left.key.Kind, right.key.Kind), cmp.Compare(left.key.ID, right.key.ID))
}

// The worst retained candidate is at the root, ready for replacement.
type candidateHeap []candidate

func (candidates candidateHeap) Len() int { return len(candidates) }
func (candidates candidateHeap) Less(left, right int) bool {
	return compareCandidates(candidates[left], candidates[right]) > 0
}
func (candidates candidateHeap) Swap(left, right int) {
	candidates[left], candidates[right] = candidates[right], candidates[left]
}
func (candidates *candidateHeap) Push(value any) {
	*candidates = append(*candidates, value.(candidate))
}
func (candidates *candidateHeap) Pop() any {
	last := len(*candidates) - 1
	value := (*candidates)[last]
	(*candidates)[last] = candidate{}
	*candidates = (*candidates)[:last]
	return value
}
func (candidates *candidateHeap) offer(value candidate) {
	// A concurrent mutation can move a resource between timer and ready queries.
	// Retain one hint per key so duplicates cannot consume the candidate window.
	for index, existing := range *candidates {
		if existing.key != value.key {
			continue
		}
		if compareCandidates(value, existing) < 0 {
			(*candidates)[index] = value
			heap.Fix(candidates, index)
		}
		return
	}
	if len(*candidates) < candidateLimit {
		heap.Push(candidates, value)
		return
	}
	if compareCandidates(value, (*candidates)[0]) < 0 {
		(*candidates)[0] = value
		heap.Fix(candidates, 0)
	}
}
