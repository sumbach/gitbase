package gitbase

import (
	"io"
	"strconv"

	"gopkg.in/src-d/go-mysql-server.v0/sql"

	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

type treeEntriesTable struct{}

// TreeEntriesSchema is the schema for the tree entries table.
var TreeEntriesSchema = sql.Schema{
	{Name: "repository_id", Type: sql.Text, Nullable: false, Source: TreeEntriesTableName},
	{Name: "tree_hash", Type: sql.Text, Nullable: false, Source: TreeEntriesTableName},
	{Name: "blob_hash", Type: sql.Text, Nullable: false, Source: TreeEntriesTableName},
	{Name: "tree_entry_mode", Type: sql.Text, Nullable: false, Source: TreeEntriesTableName},
	{Name: "tree_entry_name", Type: sql.Text, Nullable: false, Source: TreeEntriesTableName},
}

var _ sql.PushdownProjectionAndFiltersTable = (*treeEntriesTable)(nil)

func newTreeEntriesTable() sql.Table {
	return new(treeEntriesTable)
}

var _ Table = (*treeEntriesTable)(nil)

func (treeEntriesTable) isGitbaseTable() {}

func (treeEntriesTable) Resolved() bool {
	return true
}

func (treeEntriesTable) Name() string {
	return TreeEntriesTableName
}

func (treeEntriesTable) Schema() sql.Schema {
	return TreeEntriesSchema
}

func (r *treeEntriesTable) TransformUp(f sql.TransformNodeFunc) (sql.Node, error) {
	return f(r)
}

func (r *treeEntriesTable) TransformExpressionsUp(f sql.TransformExprFunc) (sql.Node, error) {
	return r, nil
}

func (r treeEntriesTable) RowIter(ctx *sql.Context) (sql.RowIter, error) {
	span, ctx := ctx.Span("gitbase.TreeEntriesTable")
	iter := new(treeEntryIter)

	repoIter, err := NewRowRepoIter(ctx, iter)
	if err != nil {
		span.Finish()
		return nil, err
	}

	return sql.NewSpanIter(span, repoIter), nil
}

func (treeEntriesTable) Children() []sql.Node {
	return nil
}

func (treeEntriesTable) HandledFilters(filters []sql.Expression) []sql.Expression {
	return handledFilters(TreeEntriesTableName, TreeEntriesSchema, filters)
}

func (r *treeEntriesTable) WithProjectAndFilters(
	ctx *sql.Context,
	_, filters []sql.Expression,
) (sql.RowIter, error) {
	span, ctx := ctx.Span("gitbase.TreeEntriesTable")
	// TODO: could be optimized even more checking that only tree_hash is
	// projected. There would be no need to iterate files in this case, and
	// it would be much faster.
	iter, err := rowIterWithSelectors(
		ctx, TreeEntriesSchema, TreeEntriesTableName, filters,
		[]string{"tree_hash"},
		func(selectors selectors) (RowRepoIter, error) {
			if len(selectors["tree_hash"]) == 0 {
				return new(treeEntryIter), nil
			}

			hashes, err := selectors.textValues("tree_hash")
			if err != nil {
				return nil, err
			}

			return &treeEntriesByHashIter{hashes: hashes}, nil
		},
	)

	if err != nil {
		span.Finish()
		return nil, err
	}

	return sql.NewSpanIter(span, iter), nil
}

func (r treeEntriesTable) String() string {
	return printTable(TreeEntriesTableName, TreeEntriesSchema)
}

type treeEntryIter struct {
	i      *object.TreeIter
	fi     *fileIter
	repoID string
}

func (i *treeEntryIter) NewIterator(repo *Repository) (RowRepoIter, error) {
	iter, err := repo.Repo.TreeObjects()
	if err != nil {
		return nil, err
	}

	return &treeEntryIter{repoID: repo.ID, i: iter}, nil
}

func (i *treeEntryIter) Next() (sql.Row, error) {
	for {
		if i.fi == nil {
			tree, err := i.i.Next()
			if err != nil {
				return nil, err
			}

			i.fi = &fileIter{repoID: i.repoID, t: tree, fi: tree.Files()}
		}

		row, err := i.fi.Next()
		if err == io.EOF {
			i.fi = nil
			continue
		} else if err != nil {
			return nil, err
		}

		return row, nil
	}
}

func (i *treeEntryIter) Close() error {
	if i.i != nil {
		i.i.Close()
	}

	return nil
}

type treeEntriesByHashIter struct {
	hashes []string
	pos    int
	repo   *Repository
	fi     *fileIter
}

func (i *treeEntriesByHashIter) NewIterator(repo *Repository) (RowRepoIter, error) {
	return &treeEntriesByHashIter{hashes: i.hashes, repo: repo}, nil
}

func (i *treeEntriesByHashIter) Next() (sql.Row, error) {
	for {
		if i.pos >= len(i.hashes) && i.fi == nil {
			return nil, io.EOF
		}

		if i.fi == nil {
			hash := plumbing.NewHash(i.hashes[i.pos])
			i.pos++
			tree, err := i.repo.Repo.TreeObject(hash)
			if err == plumbing.ErrObjectNotFound {
				continue
			}

			if err != nil {
				return nil, err
			}

			i.fi = &fileIter{repoID: i.repo.ID, t: tree, fi: tree.Files()}
		}

		row, err := i.fi.Next()
		if err == io.EOF {
			i.fi = nil
			continue
		} else if err != nil {
			return nil, err
		}

		return row, nil
	}
}

func (i *treeEntriesByHashIter) Close() error {
	return nil
}

type fileIter struct {
	repoID string
	t      *object.Tree
	fi     *object.FileIter
}

func (i *fileIter) Next() (sql.Row, error) {
	f, err := i.fi.Next()
	if err != nil {
		return nil, err
	}

	return fileToRow(i.repoID, i.t, f), nil
}

func (i *fileIter) Close() error {
	i.fi.Close()
	return nil
}

func fileToRow(repoID string, t *object.Tree, f *object.File) sql.Row {
	return sql.NewRow(
		repoID,
		t.ID().String(),
		f.Hash.String(),
		strconv.FormatInt(int64(f.Mode), 8),
		f.Name,
	)
}
