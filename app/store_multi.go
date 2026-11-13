package main

// Кілька каналів в одній БД (вебверсія): кожен канал — свій Store з тим самим з'єднанням.

// rb — запит під діалект БД. SQLite приймає «?» як є; Postgres-реалізація замінить «?» на $1, $2…
func (s *Store) rb(q string) string { return q }

// forChannel — Store іншого каналу на тому самому з'єднанні (кеш Sync — свій для кожного каналу).
func (s *Store) forChannel(id string) *Store {
	c := &Store{db: s.db, ch: id}
	c.resetCache()
	return c
}
