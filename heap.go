package van

type Heap []*Packet

func (q Heap) Len() int { return len(q) }

func (q Heap) Less(i, j int) bool { return q[i].serial < q[j].serial }

func (q Heap) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *Heap) Push(v interface{}) {
	n := len(*q)
	item := v.(*Packet)
	item.index = n
	*q = append(*q, item)
}

func (q *Heap) Pop() interface{} {
	old := *q
	n := len(old)
	item := old[n-1]
	item.index = -1
	*q = old[0 : n-1]
	return item
}
