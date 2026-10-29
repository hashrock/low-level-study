package main

import (
	"fmt"
	"sort"
)

// エグゼキュータ: AST を受け取り、B+tree を操作して結果を作る。

type Result struct {
	Columns []string // SELECT の場合のみ
	Rows    [][]Value
	Message string
}

// Exec は SQL を解析・実行する。各文の後でページをディスクに書き出す。
func (db *DB) Exec(sql string) ([]*Result, error) {
	stmts, err := Parse(sql)
	if err != nil {
		return nil, err
	}
	var results []*Result
	for _, s := range stmts {
		r, err := db.execute(s)
		if ferr := db.pager.Flush(); err == nil {
			err = ferr
		}
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (db *DB) execute(s Statement) (*Result, error) {
	switch s := s.(type) {
	case *CreateTableStmt:
		return db.execCreate(s)
	case *InsertStmt:
		return db.execInsert(s)
	case *SelectStmt:
		return db.execSelect(s)
	case *UpdateStmt:
		return db.execUpdate(s)
	case *DeleteStmt:
		return db.execDelete(s)
	}
	return nil, fmt.Errorf("unsupported statement %T", s)
}

func (db *DB) execCreate(s *CreateTableStmt) (*Result, error) {
	if _, ok := db.tables[s.Name]; ok {
		return nil, fmt.Errorf("table %s already exists", s.Name)
	}
	t := &Table{Name: s.Name, PK: -1}
	seen := map[string]bool{}
	for i, c := range s.Columns {
		if seen[c.Name] {
			return nil, fmt.Errorf("duplicate column %s", c.Name)
		}
		seen[c.Name] = true
		if c.PrimaryKey {
			if t.PK >= 0 || c.Type != TypeInt {
				return nil, fmt.Errorf("exactly one INTEGER PRIMARY KEY column is supported")
			}
			t.PK = i
		}
		t.Columns = append(t.Columns, c)
	}
	if t.PK < 0 {
		return nil, fmt.Errorf("table needs an INTEGER PRIMARY KEY column")
	}
	root, err := CreateBTree(db.pager)
	if err != nil {
		return nil, err
	}
	t.Root = root
	db.addTable(t)
	return &Result{Message: "CREATE TABLE"}, db.saveCatalog()
}

func (db *DB) execInsert(s *InsertStmt) (*Result, error) {
	t, err := db.table(s.Table)
	if err != nil {
		return nil, err
	}
	var idx []int // VALUES の i 番目がどのカラムに入るか
	if s.Columns == nil {
		for i := range t.Columns {
			idx = append(idx, i)
		}
	} else {
		for _, name := range s.Columns {
			i := t.colIndex(name)
			if i < 0 {
				return nil, fmt.Errorf("no such column: %s", name)
			}
			idx = append(idx, i)
		}
	}
	for _, exprs := range s.Rows {
		if len(exprs) != len(idx) {
			return nil, fmt.Errorf("expected %d values, got %d", len(idx), len(exprs))
		}
		vals := make([]Value, len(t.Columns))
		for j, e := range exprs {
			if vals[idx[j]], err = eval(e, nil, nil); err != nil {
				return nil, err
			}
		}
		if vals[t.PK].Kind == KindNull { // 主キー省略時は最大値 + 1 を採番
			max, ok, err := t.tree.MaxKey()
			if err != nil {
				return nil, err
			}
			vals[t.PK] = IntValue(1)
			if ok {
				vals[t.PK] = IntValue(max + 1)
			}
		}
		if err := checkTypes(t, vals); err != nil {
			return nil, err
		}
		if err := t.tree.Insert(vals[t.PK].Int, encodeRow(vals)); err != nil {
			return nil, fmt.Errorf("insert %s=%d: %w", t.Columns[t.PK].Name, vals[t.PK].Int, err)
		}
	}
	return &Result{Message: fmt.Sprintf("INSERT %d", len(s.Rows))}, nil
}

func checkTypes(t *Table, vals []Value) error {
	if vals[t.PK].Kind == KindNull {
		return fmt.Errorf("primary key cannot be NULL")
	}
	for i, c := range t.Columns {
		v := vals[i]
		if v.Kind != KindNull && (c.Type == TypeInt) != (v.Kind == KindInt) {
			return fmt.Errorf("column %s expects %s, got %q", c.Name, c.Type, v.String())
		}
	}
	return nil
}

// ---- 行の取り出し ----

// scope は「今見ている行」の各カラムがどのテーブルの何という名前か、を表す。
type scopeCol struct{ table, name string }
type scope []scopeCol

func tableScope(t *Table) scope {
	var sc scope
	for _, c := range t.Columns {
		sc = append(sc, scopeCol{t.Name, c.Name})
	}
	return sc
}

func (sc scope) resolve(ref ColumnRef) (int, error) {
	idx := -1
	for i, c := range sc {
		if c.name == ref.Name && (ref.Table == "" || ref.Table == c.table) {
			if idx >= 0 {
				return 0, fmt.Errorf("ambiguous column: %s", ref)
			}
			idx = i
		}
	}
	if idx < 0 {
		return 0, fmt.Errorf("no such column: %s", ref)
	}
	return idx, nil
}

// scanWhere は WHERE に一致する行ごとに fn を呼ぶ。
// WHERE が「主キー = 定数」なら B+tree を 1 回引くだけで済ませる (インデックス検索)。
func (db *DB) scanWhere(t *Table, where Expr, fn func(row []Value) error) error {
	sc := tableScope(t)
	visit := func(val []byte) error {
		row, err := decodeRow(val, len(t.Columns))
		if err != nil {
			return err
		}
		if ok, err := matches(where, sc, row); err != nil || !ok {
			return err
		}
		return fn(row)
	}
	if key, ok := pkEquality(t, where); ok {
		val, found, err := t.tree.Get(key)
		if err != nil || !found {
			return err
		}
		return visit(val)
	}
	return t.tree.Scan(func(_ int64, val []byte) (bool, error) {
		return true, visit(val)
	})
}

func pkEquality(t *Table, where Expr) (int64, bool) {
	b, ok := where.(*BinaryExpr)
	if !ok || b.Op != "=" {
		return 0, false
	}
	col, lit := b.Left, b.Right
	if _, isLit := col.(*Literal); isLit {
		col, lit = lit, col
	}
	c, ok1 := col.(*ColumnRef)
	l, ok2 := lit.(*Literal)
	if !ok1 || !ok2 || l.Value.Kind != KindInt || c.Name != t.Columns[t.PK].Name ||
		(c.Table != "" && c.Table != t.Name) {
		return 0, false
	}
	return l.Value.Int, true
}

func (db *DB) execSelect(s *SelectStmt) (*Result, error) {
	t1, err := db.table(s.From)
	if err != nil {
		return nil, err
	}
	sc := tableScope(t1)
	var rows [][]Value
	collect := func(row []Value) error { rows = append(rows, row); return nil }

	if s.Join == nil {
		err = db.scanWhere(t1, s.Where, collect)
	} else {
		// ネステッドループ結合: 右テーブルを全部メモリに読み、左の各行と組み合わせる。
		t2, err := db.table(s.Join.Table)
		if err != nil {
			return nil, err
		}
		sc = append(sc, tableScope(t2)...)
		var right [][]Value
		if err := db.scanWhere(t2, nil, func(r []Value) error { right = append(right, r); return nil }); err != nil {
			return nil, err
		}
		err = db.scanWhere(t1, nil, func(l []Value) error {
			for _, r := range right {
				row := append(append([]Value{}, l...), r...)
				ok, err := matches(s.Join.On, sc, row)
				if err == nil && ok {
					ok, err = matches(s.Where, sc, row)
				}
				if err != nil {
					return err
				}
				if ok {
					rows = append(rows, row)
				}
			}
			return nil
		})
	}
	if err != nil {
		return nil, err
	}

	if s.OrderBy != nil {
		i, err := sc.resolve(s.OrderBy.Column)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(rows, func(a, b int) bool {
			c := orderCompare(rows[a][i], rows[b][i])
			if s.OrderBy.Desc {
				return c > 0
			}
			return c < 0
		})
	}
	if s.Limit >= 0 && len(rows) > s.Limit {
		rows = rows[:s.Limit]
	}

	// 射影 (必要なカラムだけ取り出す)
	res := &Result{}
	var cols []int
	if s.Star {
		for i, c := range sc {
			cols = append(cols, i)
			res.Columns = append(res.Columns, c.name)
		}
	} else {
		for _, ref := range s.Columns {
			i, err := sc.resolve(ref)
			if err != nil {
				return nil, err
			}
			cols = append(cols, i)
			res.Columns = append(res.Columns, ref.String())
		}
	}
	for _, row := range rows {
		out := make([]Value, len(cols))
		for j, i := range cols {
			out[j] = row[i]
		}
		res.Rows = append(res.Rows, out)
	}
	res.Message = fmt.Sprintf("(%d rows)", len(res.Rows))
	return res, nil
}

// orderCompare は並べ替え用。NULL は最小として扱う。
func orderCompare(a, b Value) int {
	if a.Kind == KindNull || b.Kind == KindNull {
		return int(a.Kind) - int(b.Kind) // NULL(0) が先
	}
	c, _ := compare(a, b)
	return c
}

func (db *DB) execUpdate(s *UpdateStmt) (*Result, error) {
	t, err := db.table(s.Table)
	if err != nil {
		return nil, err
	}
	// 走査中に木を書き換えないよう、まず対象行を集めてから更新する。
	var targets [][]Value
	if err := db.scanWhere(t, s.Where, func(r []Value) error { targets = append(targets, r); return nil }); err != nil {
		return nil, err
	}
	sc := tableScope(t)
	for _, old := range targets {
		nv := append([]Value{}, old...)
		for _, a := range s.Sets {
			i := t.colIndex(a.Column)
			if i < 0 {
				return nil, fmt.Errorf("no such column: %s", a.Column)
			}
			if nv[i], err = eval(a.Value, sc, old); err != nil {
				return nil, err
			}
		}
		if err := checkTypes(t, nv); err != nil {
			return nil, err
		}
		oldKey, newKey := old[t.PK].Int, nv[t.PK].Int
		if newKey != oldKey {
			if _, exists, err := t.tree.Get(newKey); err != nil || exists {
				if err == nil {
					err = ErrDuplicateKey
				}
				return nil, err
			}
		}
		// 更新 = 削除 + 挿入 (行の長さが変わってもよいように)
		if _, err := t.tree.Delete(oldKey); err != nil {
			return nil, err
		}
		if err := t.tree.Insert(newKey, encodeRow(nv)); err != nil {
			return nil, err
		}
	}
	return &Result{Message: fmt.Sprintf("UPDATE %d", len(targets))}, nil
}

func (db *DB) execDelete(s *DeleteStmt) (*Result, error) {
	t, err := db.table(s.Table)
	if err != nil {
		return nil, err
	}
	var keys []int64
	if err := db.scanWhere(t, s.Where, func(r []Value) error { keys = append(keys, r[t.PK].Int); return nil }); err != nil {
		return nil, err
	}
	for _, k := range keys {
		if _, err := t.tree.Delete(k); err != nil {
			return nil, err
		}
	}
	return &Result{Message: fmt.Sprintf("DELETE %d", len(keys))}, nil
}

// ---- 式の評価 ----

func matches(e Expr, sc scope, row []Value) (bool, error) {
	if e == nil {
		return true, nil
	}
	v, err := eval(e, sc, row)
	return truthy(v), err
}

func truthy(v Value) bool { return v.Kind == KindInt && v.Int != 0 }

func boolValue(b bool) Value {
	if b {
		return IntValue(1)
	}
	return IntValue(0)
}

// eval は式を評価する。真偽値は INTEGER の 1/0、NULL との比較は NULL (= 偽) になる。
func eval(e Expr, sc scope, row []Value) (Value, error) {
	switch e := e.(type) {
	case *Literal:
		return e.Value, nil
	case *ColumnRef:
		i, err := sc.resolve(*e)
		if err != nil {
			return Value{}, err
		}
		return row[i], nil
	case *BinaryExpr:
		l, err := eval(e.Left, sc, row)
		if err != nil {
			return Value{}, err
		}
		r, err := eval(e.Right, sc, row)
		if err != nil {
			return Value{}, err
		}
		switch e.Op {
		case "AND":
			return boolValue(truthy(l) && truthy(r)), nil
		case "OR":
			return boolValue(truthy(l) || truthy(r)), nil
		}
		if l.Kind == KindNull || r.Kind == KindNull {
			return Value{}, nil
		}
		c, err := compare(l, r)
		if err != nil {
			return Value{}, err
		}
		switch e.Op {
		case "=":
			return boolValue(c == 0), nil
		case "!=":
			return boolValue(c != 0), nil
		case "<":
			return boolValue(c < 0), nil
		case ">":
			return boolValue(c > 0), nil
		case "<=":
			return boolValue(c <= 0), nil
		case ">=":
			return boolValue(c >= 0), nil
		}
	}
	return Value{}, fmt.Errorf("cannot evaluate %s", ExprString(e))
}
