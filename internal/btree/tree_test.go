package btree

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

func TestEmptyAndSingleEntry(t *testing.T) {
	var tree Tree[string]
	if value, ok := tree.Get(0); ok || value != "" {
		t.Fatalf("empty Get = %q, %v", value, ok)
	}
	if tree.Height() != 0 {
		t.Fatalf("empty height = %d", tree.Height())
	}
	tree.Ascend(func(int64, string) bool {
		t.Fatal("empty tree visited an entry")
		return true
	})
	tree.Set(0, "zero")
	if value, ok := tree.Get(0); !ok || value != "zero" {
		t.Fatalf("Get(0) = %q, %v", value, ok)
	}
	if tree.Height() != 1 {
		t.Fatalf("single entry height = %d", tree.Height())
	}
	checkInvariants(t, &tree)
}

func TestMultipleLevelsAndInsertionOrders(t *testing.T) {
	const count = 12000
	orders := map[string][]int{
		"ascending":  make([]int, count),
		"descending": make([]int, count),
		"random":     rand.New(rand.NewSource(27)).Perm(count),
	}
	for i := 0; i < count; i++ {
		orders["ascending"][i] = i
		orders["descending"][i] = count - i - 1
	}
	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			var tree Tree[int]
			want := make(map[int64]int)
			for i, key := range order {
				// Negative and positive, non-contiguous keys exercise searches
				// between entries as well as insertion at both ends.
				k := int64(key*3 - count)
				tree.Set(k, key+1)
				want[k] = key + 1
				if i%317 == 0 {
					checkInvariants(t, &tree)
				}
			}
			if tree.Height() < 4 {
				t.Fatalf("expected multiple internal levels, height = %d", tree.Height())
			}
			checkInvariants(t, &tree)
			checkAgainstMap(t, &tree, want)
			for key := range want {
				if _, ok := tree.Get(key + 1); ok {
					t.Fatalf("missing key %d unexpectedly exists", key+1)
				}
			}
		})
	}
}

func TestReplacementsDoNotSplitOrDuplicate(t *testing.T) {
	var tree Tree[int]
	want := make(map[int64]int)
	for i := 0; i < 3000; i++ {
		tree.Set(int64(i), i)
		want[int64(i)] = i
	}
	height := tree.Height()
	for round := 0; round < 3; round++ {
		for i := 2999; i >= 0; i-- {
			value := -(i + round + 1)
			tree.Set(int64(i), value)
			want[int64(i)] = value
		}
	}
	if tree.Height() != height {
		t.Fatalf("replacement changed height from %d to %d", height, tree.Height())
	}
	checkInvariants(t, &tree)
	checkAgainstMap(t, &tree, want)
}

func TestExtremaAndEarlyStop(t *testing.T) {
	var tree Tree[string]
	keys := []int64{math.MaxInt64, 0, math.MinInt64, -1, 1}
	for _, key := range keys {
		tree.Set(key, "value")
	}
	checkInvariants(t, &tree)
	for _, key := range keys {
		if value, ok := tree.Get(key); !ok || value != "value" {
			t.Fatalf("Get(%d) = %q, %v", key, value, ok)
		}
	}
	var visited []int64
	tree.Ascend(func(key int64, _ string) bool {
		visited = append(visited, key)
		return len(visited) < 3
	})
	want := []int64{math.MinInt64, -1, 0}
	for i, key := range want {
		if len(visited) != len(want) || visited[i] != key {
			t.Fatalf("early-stop traversal = %v, want %v", visited, want)
		}
	}
	// Also stop while traversing an internal subtree, not just a single leaf.
	var large Tree[int]
	for i := 0; i < 2000; i++ {
		large.Set(int64(i), i)
	}
	count := 0
	large.Ascend(func(key int64, value int) bool {
		if key != int64(count) || value != count {
			t.Fatalf("visit %d = %d, %d", count, key, value)
		}
		count++
		return count < 97
	})
	if count != 97 {
		t.Fatalf("visited %d entries after stop, want 97", count)
	}
}

func checkAgainstMap(t *testing.T, tree *Tree[int], want map[int64]int) {
	t.Helper()
	keys := make([]int64, 0, len(want))
	for key, expected := range want {
		keys = append(keys, key)
		if value, ok := tree.Get(key); !ok || value != expected {
			t.Fatalf("Get(%d) = %d, %v; want %d, true", key, value, ok, expected)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	visited := 0
	tree.Ascend(func(key int64, value int) bool {
		if visited >= len(keys) || key != keys[visited] || value != want[key] {
			t.Fatalf("unexpected visit %d: key %d value %d", visited, key, value)
		}
		visited++
		return true
	})
	if visited != len(want) {
		t.Fatalf("visited %d entries, want %d", visited, len(want))
	}
}

// Check structural properties independently from Get and Ascend so those
// operations cannot mask a split that violates B-tree balancing or key bounds.
func checkInvariants[V any](t *testing.T, tree *Tree[V]) {
	t.Helper()
	if tree.root == nil {
		return
	}
	leafDepth := -1
	var walk func(*node[V], int, *int64, *int64)
	walk = func(n *node[V], depth int, lower, upper *int64) {
		if n == nil {
			t.Fatal("missing child")
		}
		minimum := minimumDegree - 1
		if n == tree.root {
			minimum = 1
		}
		if n.count < minimum || n.count > maxKeys {
			t.Fatalf("node occupancy %d at depth %d", n.count, depth)
		}
		for i := 0; i < n.count; i++ {
			if (i > 0 && n.keys[i-1] >= n.keys[i]) ||
				(lower != nil && n.keys[i] <= *lower) ||
				(upper != nil && n.keys[i] >= *upper) {
				t.Fatalf("unordered or out-of-bounds key %d at depth %d", n.keys[i], depth)
			}
		}
		if n.leaf {
			if leafDepth < 0 {
				leafDepth = depth
			} else if depth != leafDepth {
				t.Fatalf("leaf depths differ: %d and %d", leafDepth, depth)
			}
			for _, child := range n.children {
				if child != nil {
					t.Fatal("leaf has a child")
				}
			}
			return
		}
		for i := 0; i <= n.count; i++ {
			childLower, childUpper := lower, upper
			if i > 0 {
				childLower = &n.keys[i-1]
			}
			if i < n.count {
				childUpper = &n.keys[i]
			}
			walk(n.children[i], depth+1, childLower, childUpper)
		}
		for i := n.count + 1; i < len(n.children); i++ {
			if n.children[i] != nil {
				t.Fatal("node retains child beyond occupancy")
			}
		}
	}
	walk(tree.root, 0, nil, nil)
	if tree.Height() != leafDepth+1 {
		t.Fatalf("Height = %d, actual = %d", tree.Height(), leafDepth+1)
	}
}
