Write a function `lookup(env, key)` that returns the value of `key` in a `map[string]string`,
or null when the key is missing (a nullable string). Write
`address(host, port, secure)` that formats a database address, where `port` defaults to 5432
and `secure` to false; a secure address uses the scheme `postgres+tls`.

For each of these environments, in order, print the address built from `DB_HOST` (default
`localhost`), `DB_PORT` and `DB_TLS` (`on` means secure):

1. `DB_HOST=db.internal`, `DB_PORT=6543`, `DB_TLS=on`
2. `DB_HOST=cache`
3. `DB_PORT=7000`
4. `DB_HOST=api`, `DB_PORT=eighty`
5. nothing set

When `DB_PORT` is missing, call `address` without a port, naming the `secure` argument
(`secure = ...`) so the default port applies. When the port isn't a number, print the line
shown below instead of an address.

The program must print exactly:

```
postgres+tls://db.internal:6543
postgres://cache:5432
postgres://localhost:7000
api: bad port eighty
postgres://localhost:5432
```
