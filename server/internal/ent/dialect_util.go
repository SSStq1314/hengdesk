package ent

// SQLDialect 返回底层驱动的方言名（postgres / sqlite3），供调用方按库实现差异化路径。
func (c *Client) SQLDialect() string { return c.driver.Dialect() }
