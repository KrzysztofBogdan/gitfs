# dns: cloudflare zone

One file per zone, one `<record>` per DNS record. Provider attributes that
BIND cannot express (`proxied`) are ordinary attributes here, so nothing rides
in comments.

```shell
$ gfs clone cloudflare://example.com dns
$ ls dns
example.com.xml
```

## Change records

```shell
$ vim example.com.xml   # bump the A record, add a CNAME, drop old MX
$ gfs diff
--- example.com.xml (base)
+++ example.com.xml (local)
-  <record id="a1f0…" type="A" name="@" ttl="300" proxied="true">203.0.113.10</record>
+  <record id="a1f0…" type="A" name="@" ttl="300" proxied="true">203.0.113.11</record>
+  <record type="CNAME" name="status" ttl="300">statuspage.io.</record>
-  <record id="9c3b…" type="MX" name="@" ttl="300" priority="20">mx2.old-provider.net.</record>

$ gfs status
  M  example.com.xml
       update   A     @        203.0.113.10 -> 203.0.113.11
       create   CNAME status   -> statuspage.io.
       delete   MX    @        mx2.old-provider.net.                [ask]

$ gfs commit --allow delete
update  A     @        ok
create  CNAME status   ok   id=7e1a…
delete  MX    @        ok
3 actions, 0 failed
```

`id` is Cloudflare's record id and read-only; a `<record>` without `id` is
new. `pull` rewrites the file in canonical order (by name, then type), so a
local reorder shows no diff.

A BIND zone-file view (`gfs show --zone example.com.xml`) would be a cheap
read-only add-on; the XML is the truth.
