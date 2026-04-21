package queuetest

import (
	"container/heap"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type queueEntry struct {
	job   *queue.Job
	index int
}

type priorityQueue []*queueEntry

var _ heap.Interface = (*priorityQueue)(nil)

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	if pq[i].job.Priority != pq[j].job.Priority {
		return pq[i].job.Priority > pq[j].job.Priority
	}
	return pq[i].job.Seq < pq[j].job.Seq
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	entry := x.(*queueEntry)
	entry.index = len(*pq)
	*pq = append(*pq, entry)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*pq = old[:n-1]
	return entry
}

func (pq priorityQueue) position(jobID string) int {
	type entry struct {
		id  string
		pri int
		seq uint64
	}
	entries := make([]entry, pq.Len())
	for i, e := range pq {
		entries[i] = entry{e.job.ID, e.job.Priority, e.job.Seq}
	}
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].pri > entries[i].pri || (entries[j].pri == entries[i].pri && entries[j].seq < entries[i].seq) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	for i, e := range entries {
		if e.id == jobID {
			return i + 1
		}
	}
	return 0
}
