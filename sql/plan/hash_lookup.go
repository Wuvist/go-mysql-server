// Copyright 2021 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plan

import (
	"sync"

	"github.com/dolthub/go-mysql-server/sql"
)

// NewHashLookup returns a node that performs an indexed hash lookup
// of cached rows for fulfilling RowIter() calls. In particular, this
// node sits directly on top of a `CachedResults` node and has two
// expressions: a projection for hashing the Child row results and
// another projection for hashing the parent row values when
// performing a lookup. When RowIter is called, if cached results are
// available, it fulfills the RowIter call by performing a hash lookup
// on the projected results. If cached results are not available, it
// simply delegates to the child.
func NewHashLookup(n *CachedResults, childProjection sql.Expression, lookupProjection sql.Expression) *HashLookup {
	return &HashLookup{
		UnaryNode: UnaryNode{n},
		childProjection: childProjection,
		lookupProjection: lookupProjection,
		mutex: new(sync.Mutex),
	}
}

type HashLookup struct {
	UnaryNode
	childProjection  sql.Expression
	lookupProjection sql.Expression
	mutex            *sync.Mutex
	lookup           map[interface{}][]sql.Row
}

func (n *HashLookup) String() string {
	pr := sql.NewTreePrinter()
	_ = pr.WriteNode("HashLookup(child: %v, lookup: %v)", n.childProjection, n.lookupProjection)
	_ = pr.WriteChildren(n.UnaryNode.Child.String())
	return pr.String()
}

func (n *HashLookup) DebugString() string {
	pr := sql.NewTreePrinter()
	_ = pr.WriteNode("HashLookup(child: %v, lookup: %v)", sql.DebugString(n.childProjection), sql.DebugString(n.lookupProjection))
	_ = pr.WriteChildren(sql.DebugString(n.UnaryNode.Child))
	return pr.String()
}

func (n *HashLookup) WithChildren(children ...sql.Node) (sql.Node, error) {
	if len(children) != 1 {
		return nil, sql.ErrInvalidChildrenNumber.New(n, len(children), 1)
	}
	if _, ok := children[0].(*CachedResults); !ok {
		return nil, sql.ErrInvalidChildType.New(n, children[0], (*CachedResults)(nil))
	}
	nn := *n
	nn.UnaryNode.Child = children[0]
	return &nn, nil
}

func (n *HashLookup) RowIter(ctx *sql.Context, r sql.Row) (sql.RowIter, error) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	if n.lookup == nil {
		if res := n.UnaryNode.Child.(*CachedResults).getCachedResults(); res != nil {
			n.lookup = make(map[interface{}][]sql.Row)
			for _, row := range res {
				// TODO: Maybe do not put nil stuff in here.
				key, err := n.childProjection.Eval(ctx, row)
				if err != nil {
					return nil, err
				}
				n.lookup[key] = append(n.lookup[key], row)
			}
			// TODO: After the row cache is consumed and
			// hashed, it would be nice to dispose it. It
			// will never be used again.
		}
	}
	if n.lookup != nil {
		key, err := n.lookupProjection.Eval(ctx, r)
		if err != nil {
			return nil, err
		}
		return sql.RowsToRowIter(n.lookup[key]...), nil
	}
	return n.UnaryNode.Child.RowIter(ctx, r)
}
