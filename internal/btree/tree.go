// Package btree provides a small, in-memory ordered index built from scratch.
// A Tree is not safe for concurrent access; callers must provide synchronization.
package btree

const (
	minimumDegree = 8
	maxKeys       = 2*minimumDegree - 1
)

type node[V any] struct {
	keys     [maxKeys]int64
	values   [maxKeys]V
	children [maxKeys + 1]*node[V]
	count    int
	leaf     bool
}

// Tree is a B-tree mapping signed integer keys to values. Its zero value is an
// empty, usable tree. Values are replaced by Set; deletion is not provided.
// A populated Tree must not be copied, because copies would share its nodes.
type Tree[V any] struct {
	root *node[V]
}

// Get returns the value at key, and whether the key exists.
func (t *Tree[V]) Get(key int64) (V, bool) {
	if n, i := t.find(key); n != nil {
		return n.values[i], true
	}
	var zero V
	return zero, false
}

// Set inserts key or replaces its existing value.
func (t *Tree[V]) Set(key int64, value V) {
	// Replace in place before any split. Updates do not change tree structure.
	if n, i := t.find(key); n != nil {
		n.values[i] = value
		return
	}
	if t.root == nil {
		t.root = &node[V]{leaf: true}
	}
	if t.root.count == maxKeys {
		newRoot := &node[V]{}
		newRoot.children[0] = t.root
		newRoot.splitChild(0)
		t.root = newRoot
	}
	t.root.insertNonFull(key, value)
}

// Ascend visits entries in increasing key order. Returning false from visit
// stops the traversal immediately. The callback must not mutate the tree.
func (t *Tree[V]) Ascend(visit func(key int64, value V) bool) {
	if t.root != nil {
		t.root.ascend(visit)
	}
}

// Height returns the number of node levels, or zero for an empty tree.
func (t *Tree[V]) Height() int {
	height := 0
	for n := t.root; n != nil; n = n.children[0] {
		height++
		if n.leaf {
			break
		}
	}
	return height
}

func (t *Tree[V]) find(key int64) (*node[V], int) {
	for n := t.root; n != nil; {
		i := n.lowerBound(key)
		if i < n.count && n.keys[i] == key {
			return n, i
		}
		if n.leaf {
			break
		}
		n = n.children[i]
	}
	return nil, 0
}

func (n *node[V]) lowerBound(key int64) int {
	low, high := 0, n.count
	for low < high {
		middle := low + (high-low)/2
		if n.keys[middle] < key {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low
}

// splitChild promotes the median of a full child into this non-full node.
// Both resulting children contain minimumDegree-1 keys.
func (n *node[V]) splitChild(index int) {
	left := n.children[index]
	right := &node[V]{leaf: left.leaf, count: minimumDegree - 1}
	medianKey := left.keys[minimumDegree-1]
	medianValue := left.values[minimumDegree-1]
	var zero V
	for j := 0; j < minimumDegree-1; j++ {
		right.keys[j] = left.keys[j+minimumDegree]
		right.values[j] = left.values[j+minimumDegree]
	}
	if !left.leaf {
		for j := 0; j < minimumDegree; j++ {
			right.children[j] = left.children[j+minimumDegree]
			left.children[j+minimumDegree] = nil
		}
	}
	for j := minimumDegree - 1; j < maxKeys; j++ {
		left.keys[j] = 0
		left.values[j] = zero
	}
	left.count = minimumDegree - 1
	for j := n.count; j > index; j-- {
		n.children[j+1] = n.children[j]
	}
	n.children[index+1] = right
	for j := n.count; j > index; j-- {
		n.keys[j] = n.keys[j-1]
		n.values[j] = n.values[j-1]
	}
	n.keys[index] = medianKey
	n.values[index] = medianValue
	n.count++
}

// insertNonFull descends only into non-full nodes, splitting on the way down.
// Its key is absent because Set handles replacements before insertion.
func (n *node[V]) insertNonFull(key int64, value V) {
	i := n.lowerBound(key)
	if n.leaf {
		for j := n.count; j > i; j-- {
			n.keys[j] = n.keys[j-1]
			n.values[j] = n.values[j-1]
		}
		n.keys[i], n.values[i] = key, value
		n.count++
		return
	}
	if n.children[i].count == maxKeys {
		n.splitChild(i)
		if key > n.keys[i] {
			i++
		}
	}
	n.children[i].insertNonFull(key, value)
}

func (n *node[V]) ascend(visit func(int64, V) bool) bool {
	for i := 0; i < n.count; i++ {
		if !n.leaf && !n.children[i].ascend(visit) {
			return false
		}
		if !visit(n.keys[i], n.values[i]) {
			return false
		}
	}
	return n.leaf || n.children[n.count].ascend(visit)
}
