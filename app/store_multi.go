package main

import (
	"strconv"
	"strings"
)

// Кілька каналів в одній БД (вебверсія): кожен канал — свій Store з тим самим з'єднанням.
//
// Діалекти (SQLite / PostgreSQL) розходяться лише тут: rb (плейсхолдери), ddl (колонка порядку в схемі) і seq (вираз сортування).

// rb — запит під діалект БД. SQLite приймає «?» як є; для Postgres «?» стає $1, $2…
// (у запитах немає «?» усередині рядкових літералів).
func (s *Store) rb(q string) string {
	if !s.pg || !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

// ddl — підставляє в схему колонку порядку вставки. У SQLite порядок дає вбудований rowid
// (наявні файли phillbot.db лишаються без змін), у Postgres — seq BIGSERIAL.
func (s *Store) ddl(q string) string {
	if s.pg {
		return strings.ReplaceAll(q, "%SEQ%", ", seq BIGSERIAL")
	}
	return strings.ReplaceAll(q, "%SEQ%", "")
}

// seq — вираз для ORDER BY «у порядку вставки» (entries, bids, seen).
func (s *Store) seq() string {
	if s.pg {
		return "seq"
	}
	return "rowid"
}

// forChannel — Store іншого каналу на тому самому з'єднанні (кеш Sync — свій для кожного каналу).
func (s *Store) forChannel(id string) *Store {
	c := &Store{db: s.db, ch: id, pg: s.pg}
	c.resetCache()
	return c
}
