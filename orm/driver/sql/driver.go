package sql

import (
	"bytes"
	"database/sql"
	"fmt"
	"gnd.la/config"
	"gnd.la/log"
	"gnd.la/orm/codec"
	"gnd.la/orm/driver"
	"gnd.la/orm/index"
	"gnd.la/orm/query"
	"reflect"
	"strconv"
	"strings"
)

var (
	stringType = reflect.TypeOf("")
)

type Driver struct {
	db         *db
	conn       DB
	logger     *log.Logger
	backend    Backend
	transforms map[reflect.Type]struct{}
}

func (d *Driver) MakeTables(ms []driver.Model) error {
	// Create tables
	// TODO: References
	for _, v := range ms {
		tableFields, err := d.tableFields(v)
		if err != nil {
			return err
		}
		if len(tableFields) == 0 {
			log.Debugf("Skipping collection %s (model %v) because it has no fields", v.Table, v)
			continue
		}
		if cpk := v.Fields().CompositePrimaryKey; len(cpk) > 0 {
			var pkFields []string
			qnames := v.Fields().MNames
			for _, f := range cpk {
				pkFields = append(pkFields, fmt.Sprintf("\"%s\"", qnames[f]))
			}
			tableFields = append(tableFields, fmt.Sprintf("PRIMARY KEY(%s)", strings.Join(pkFields, ",")))
		}
		sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS \"%s\" (\n%s\n);", v.Table(), strings.Join(tableFields, ",\n"))
		_, err = d.db.Exec(sql)
		if err != nil {
			return err
		}
	}
	// Create indexes
	for _, v := range ms {
		for _, idx := range v.Indexes() {
			name, err := d.indexName(v, idx)
			if err != nil {
				return err
			}
			err = d.backend.Index(d.db, v, idx, name)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Driver) Query(m driver.Model, q query.Q, limit int, offset int, sort int, sortField string) driver.Iter {
	query, params, err := d.Select(m.Fields().MNames, true, m, q, limit, offset, sort, sortField)
	if err != nil {
		return &Iter{err: err}
	}
	rows, err := d.db.Query(query, params...)
	if err != nil {
		return &Iter{err: err}
	}
	return &Iter{model: m, rows: rows, driver: d}
}

func (d *Driver) Count(m driver.Model, q query.Q, limit int, offset int) (uint64, error) {
	var count uint64
	query, params, err := d.Select([]string{"COUNT(*)"}, false, m, q, limit, offset, driver.NONE, "")
	if err != nil {
		return 0, err
	}
	err = d.db.QueryRow(query, params...).Scan(&count)
	return count, err
}

func (d *Driver) Exists(m driver.Model, q query.Q) (bool, error) {
	query, params, err := d.Select([]string{"1"}, false, m, q, -1, -1, driver.NONE, "")
	if err != nil {
		return false, err
	}
	var one uint64
	err = d.db.QueryRow(query, params...).Scan(&one)
	if err == sql.ErrNoRows {
		err = nil
	}
	return one == 1, err
}

func (d *Driver) Insert(m driver.Model, data interface{}) (driver.Result, error) {
	_, fields, values, err := d.saveParameters(m, data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("INSERT INTO ")
	buf.WriteByte('"')
	buf.WriteString(m.Table())
	buf.WriteByte('"')
	buf.WriteString(" (")
	for _, v := range fields {
		buf.WriteByte('"')
		buf.WriteString(v)
		buf.WriteByte('"')
		buf.WriteByte(',')
	}
	buf.Truncate(buf.Len() - 1)
	buf.WriteString(") VALUES (")
	buf.WriteString(d.backend.Placeholders(len(fields)))
	buf.WriteByte(')')
	return d.backend.Insert(d.db, m, buf.String(), values...)
}

func (d *Driver) Update(m driver.Model, q query.Q, data interface{}) (driver.Result, error) {
	_, fields, values, err := d.saveParameters(m, data)
	if err != nil {
		return nil, err
	}
	where, qParams, err := d.where(m, q, len(values))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("UPDATE ")
	buf.WriteByte('"')
	buf.WriteString(m.Table())
	buf.WriteByte('"')
	buf.WriteString(" SET ")
	for ii, v := range fields {
		buf.WriteString(v)
		buf.WriteByte('=')
		buf.WriteString(d.backend.Placeholder(ii + 1))
		buf.WriteByte(',')
	}
	// remove last ,
	buf.Truncate(buf.Len() - 1)
	if where != "" {
		buf.WriteString(" WHERE ")
		buf.WriteString(where)
	}
	params := append(values, qParams...)
	return d.db.Exec(buf.String(), params...)
}

func (d *Driver) Upsert(m driver.Model, q query.Q, data interface{}) (driver.Result, error) {
	// TODO: MySql might be able to provide upserts
	return nil, nil
}

func (d *Driver) Delete(m driver.Model, q query.Q) (driver.Result, error) {
	where, params, err := d.where(m, q, 0)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("DELETE FROM ")
	buf.WriteByte('"')
	buf.WriteString(m.Table())
	buf.WriteByte('"')
	if where != "" {
		buf.WriteString(" WHERE ")
		buf.WriteString(where)
	}
	return d.db.Exec(buf.String(), params...)
}

func (d *Driver) Close() error {
	return d.db.Close()
}

func (d *Driver) Upserts() bool {
	return false
}

func (d *Driver) Tags() []string {
	return []string{d.backend.Tag(), "sql"}
}

func (d *Driver) DB() *sql.DB {
	return d.db.DB
}

func (d *Driver) DBBackend() Backend {
	return d.backend
}

func (d *Driver) SetLogger(logger *log.Logger) {
	d.logger = logger
}

func (d *Driver) debugq(sql string, args []interface{}) {
	if d.logger != nil {
		d.logger.Debugf("SQL %q with arguments %v", sql, args)
	}
}

func (d *Driver) fieldByIndex(val reflect.Value, indexes []int, alloc bool) reflect.Value {
	for _, v := range indexes {
		if val.Type().Kind() == reflect.Ptr {
			if val.IsNil() {
				if !alloc {
					return reflect.Value{}
				}
				val.Set(reflect.New(val.Type().Elem()))
			}
			val = val.Elem()
		}
		val = val.Field(v)
	}
	return val
}

func (d *Driver) saveParameters(m driver.Model, data interface{}) (reflect.Value, []string, []interface{}, error) {
	// data is guaranteed to be of m.Type()
	val := driver.Direct(reflect.ValueOf(data))
	fields := m.Fields()
	max := len(fields.MNames)
	names := make([]string, 0, max)
	values := make([]interface{}, 0, max)
	var err error
	if d.transforms != nil {
		for ii, v := range fields.Indexes {
			f := d.fieldByIndex(val, v, false)
			if !f.IsValid() {
				continue
			}
			if fields.OmitEmpty[ii] && driver.IsZero(f) {
				continue
			}
			ft := f.Type()
			var fval interface{}
			if _, ok := d.transforms[ft]; ok {
				fval, err = d.backend.TransformOutValue(f)
				if err != nil {
					return val, nil, nil, err
				}
				if fields.NullEmpty[ii] && driver.IsZero(reflect.ValueOf(fval)) {
					fval = nil
				}
			} else if !fields.NullEmpty[ii] || !driver.IsZero(f) {
				if c := codec.FromTag(fields.Tags[ii]); c != nil {
					fval, err = c.Encode(&f)
					if err != nil {
						return val, nil, nil, err
					}
				} else {
					// Most sql drivers won't accept aliases for string type
					if ft.Kind() == reflect.String && ft != stringType {
						f = f.Convert(stringType)
					}
					fval = f.Interface()
				}
			}
			names = append(names, fields.MNames[ii])
			values = append(values, fval)
		}
	} else {
		for ii, v := range fields.Indexes {
			f := d.fieldByIndex(val, v, false)
			if !f.IsValid() {
				continue
			}
			if fields.OmitEmpty[ii] && driver.IsZero(f) {
				continue
			}
			var fval interface{}
			if !fields.NullEmpty[ii] || !driver.IsZero(f) {
				if c := codec.FromTag(fields.Tags[ii]); c != nil {
					fval, err = c.Encode(&f)
					if err != nil {
						return val, nil, nil, err
					}
				} else {
					ft := f.Type()
					// Most sql drivers won't accept aliases for string type
					if ft.Kind() == reflect.String && ft != stringType {
						f = f.Convert(stringType)
					}
					fval = f.Interface()
				}
			}
			names = append(names, fields.MNames[ii])
			values = append(values, fval)
		}
	}
	return val, names, values, nil
}

func (d *Driver) outValues(m driver.Model, out interface{}) (reflect.Value, *driver.Fields, []interface{}, []scanner, error) {
	val := reflect.ValueOf(out)
	vt := val.Type()
	if vt.Kind() != reflect.Ptr {
		return reflect.Value{}, nil, nil, nil, fmt.Errorf("can't set object of type %t. Please, pass a %v rather than a %v", out, reflect.PtrTo(vt), vt)
	}
	if vt.Elem().Kind() == reflect.Ptr && vt.Elem().Elem().Kind() == reflect.Struct {
		// Received a pointer to pointer. Always create a new object,
		// to avoid overwriting the previous result.
		val = val.Elem()
		el := reflect.New(val.Type().Elem())
		val.Set(el)
	}
	for val.Kind() == reflect.Ptr {
		el := val.Elem()
		if !el.IsValid() {
			el = reflect.New(val.Type().Elem())
			val.Set(el)
		}
		val = el
	}
	fields := m.Fields()
	values := make([]interface{}, len(fields.Indexes))
	scanners := make([]scanner, len(fields.Indexes))
	for ii, v := range fields.Indexes {
		field := d.fieldByIndex(val, v, true)
		tag := fields.Tags[ii]
		var s scanner
		if _, ok := d.transforms[field.Type()]; ok {
			s = BackendScanner(&field, tag, d.backend)
		} else {
			s = Scanner(&field, tag)
		}
		scanners[ii] = s
		values[ii] = s
	}
	return val, fields, values, scanners, nil
}

func (d *Driver) tableFields(m driver.Model) ([]string, error) {
	fields := m.Fields()
	names := fields.MNames
	types := fields.Types
	tags := fields.Tags
	dbFields := make([]string, len(names))
	for ii, v := range names {
		typ := types[ii]
		tag := tags[ii]
		ft, err := d.backend.FieldType(typ, tag)
		if err != nil {
			return nil, err
		}
		opts, err := d.backend.FieldOptions(typ, tag)
		if err != nil {
			return nil, err
		}
		dbFields[ii] = fmt.Sprintf("\"%s\" %s %s", v, ft, strings.Join(opts, " "))
	}
	return dbFields, nil
}

func (d *Driver) where(m driver.Model, q query.Q, begin int) (string, []interface{}, error) {
	var params []interface{}
	clause, err := d.condition(m.Fields(), q, &params, begin)
	return clause, params, err
}

func (d *Driver) condition(fields *driver.Fields, q query.Q, params *[]interface{}, begin int) (string, error) {
	var clause string
	var err error
	switch x := q.(type) {
	case *query.Eq:
		if isNil(x.Value) {
			x.Value = nil
			clause, err = d.clause(fields, "%s IS NULL", &x.Field, params, begin)
		} else {
			clause, err = d.clause(fields, "%s = %s", &x.Field, params, begin)
		}
	case *query.Neq:
		if isNil(x.Value) {
			x.Value = nil
			clause, err = d.clause(fields, "%s IS NOT NULL", &x.Field, params, begin)
		} else {
			clause, err = d.clause(fields, "%s != %s", &x.Field, params, begin)
		}
	case *query.Lt:
		clause, err = d.clause(fields, "%s < %s", &x.Field, params, begin)
	case *query.Lte:
		clause, err = d.clause(fields, "%s <= %s", &x.Field, params, begin)
	case *query.Gt:
		clause, err = d.clause(fields, "%s > %s", &x.Field, params, begin)
	case *query.Gte:
		clause, err = d.clause(fields, "%s >= %s", &x.Field, params, begin)
	case *query.In:
		value := reflect.ValueOf(x.Value)
		if value.Type().Kind() != reflect.Slice {
			return "", fmt.Errorf("argument for IN must be a slice (field %s)", x.Field.Field)
		}
		dbName, _, err := fields.Map(x.Field.Field)
		if err != nil {
			return "", err
		}
		vLen := value.Len()
		placeholders := make([]string, vLen)
		jj := len(*params) + begin + 1
		for ii := 0; ii < vLen; ii++ {
			*params = append(*params, value.Index(ii).Interface())
			placeholders[ii] = d.backend.Placeholder(jj)
			jj++
		}
		clause = fmt.Sprintf("%s IN (%s)", dbName, strings.Join(placeholders, ","))
	case *query.And:
		clause, err = d.conditions(fields, x.Conditions, " AND ", params, begin)
	case *query.Or:
		clause, err = d.conditions(fields, x.Conditions, " OR ", params, begin)
	}
	return clause, err
}

func (d *Driver) clause(fields *driver.Fields, format string, f *query.Field, params *[]interface{}, begin int) (string, error) {
	dbName, _, err := fields.Map(f.Field)
	if err != nil {
		return "", err
	}
	if f.Value != nil {
		*params = append(*params, f.Value)
		return fmt.Sprintf(format, dbName, d.backend.Placeholder(len(*params)+begin)), nil
	}
	return fmt.Sprintf(format, dbName), nil
}

func (d *Driver) conditions(fields *driver.Fields, q []query.Q, sep string, params *[]interface{}, begin int) (string, error) {
	clauses := make([]string, len(q))
	for ii, v := range q {
		c, err := d.condition(fields, v, params, begin)
		if err != nil {
			return "", err
		}
		clauses[ii] = c
	}
	return fmt.Sprintf("(%s)", strings.Join(clauses, sep)), nil
}

func (d *Driver) indexName(m driver.Model, idx *index.Index) (string, error) {
	if len(idx.Fields) == 0 {
		return "", fmt.Errorf("index on %v has no fields", m.Type())
	}
	var buf bytes.Buffer
	buf.WriteString(m.Table())
	fields := m.Fields()
	for _, v := range idx.Fields {
		dbName, _, err := fields.Map(v)
		if err != nil {
			return "", err
		}
		buf.WriteByte('_')
		buf.WriteString(dbName)
	}
	return buf.String(), nil
}

func (d *Driver) Select(fields []string, quote bool, m driver.Model, q query.Q, limit int, offset int, sort int, sortField string) (string, []interface{}, error) {
	where, params, err := d.where(m, q, 0)
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("SELECT ")
	if quote {
		for _, v := range fields {
			buf.WriteByte('"')
			buf.WriteString(v)
			buf.WriteByte('"')
			buf.WriteByte(',')
		}
	} else {
		for _, v := range fields {
			buf.WriteString(v)
			buf.WriteByte(',')
		}
	}
	buf.Truncate(buf.Len() - 1)
	buf.WriteString(" FROM ")
	buf.WriteByte('"')
	buf.WriteString(m.Table())
	buf.WriteByte('"')
	if where != "" {
		buf.WriteString(" WHERE ")
		buf.WriteString(where)
	}
	if sort != driver.NONE && sortField != "" {
		buf.WriteString(" ORDER BY ")
		dbName, _, err := m.Fields().Map(sortField)
		if err != nil {
			return "", nil, err
		}
		buf.WriteString(dbName)
		switch sort {
		case driver.ASC:
			buf.WriteString(" ASC")
		case driver.DESC:
			buf.WriteString(" DESC")
		}
	}
	if limit >= 0 {
		buf.WriteString(" LIMIT ")
		buf.WriteString(strconv.Itoa(limit))
	}
	if offset >= 0 {
		buf.WriteString(" OFFSET ")
		buf.WriteString(strconv.Itoa(offset))
	}
	return buf.String(), params, nil
}

func (d *Driver) Begin() (driver.Tx, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	driver := &Driver{
		logger:     d.logger,
		backend:    d.backend,
		transforms: d.transforms,
	}
	driver.db = &db{
		DB:     d.db.DB,
		tx:     tx,
		db:     tx,
		driver: driver,
	}
	return driver, nil
}

func (d *Driver) Commit() error {
	if d.db.tx == nil {
		return driver.ErrNotInTransaction
	}
	return d.db.tx.Commit()
}

func (d *Driver) Rollback() error {
	if d.db.tx == nil {
		return driver.ErrNotInTransaction
	}
	return d.db.tx.Rollback()
}

func NewDriver(b Backend, url *config.URL) (*Driver, error) {
	conn, err := sql.Open(b.Name(), url.Value)
	if err != nil {
		return nil, err
	}
	if opts := url.Options; opts != nil {
		if mc, ok := opts.Int("max_conns"); ok {
			setMaxConns(conn, mc)
		}
		if mic, ok := opts.Int("max_idle_conns"); ok {
			conn.SetMaxIdleConns(mic)
		}
	}
	var transforms map[reflect.Type]struct{}
	if tt := b.Transforms(); len(tt) > 0 {
		transforms = make(map[reflect.Type]struct{}, len(tt)*2)
		for _, v := range tt {
			transforms[v] = struct{}{}
			transforms[v.Elem()] = struct{}{}
		}
	}
	driver := &Driver{backend: b, transforms: transforms}
	driver.db = &db{DB: conn, db: conn, driver: driver}
	return driver, nil
}