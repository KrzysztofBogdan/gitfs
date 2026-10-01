# dns: OVH and ClouDNS zones

One file per zone. Each element keeps its provider id; an element without
`id` is new. Both trees below have one pending record (`api`, no `id`).

```shell
$ gfs auth login ovh://eu
$ gfs clone ovh://eu
Cloned 1 resources from ovh://eu into ovh-eu
$ gfs clone cloudns://sub-1234
Cloned 2 resources from cloudns://sub-1234 into cloudns-sub-1234
```

## OVH (`ovh-eu/`)

MX and SRV parameters live inside the value, as OVH stores them. The
redirect and the DynHost entry are backed by records OVH manages; those
records are not shown as `<record>`.

```shell
$ cd ovh-eu && gfs status
  M  domain/zone/example.com.xml
        create record api A 203.0.113.20 ttl 300
$ gfs commit
create domain/zone/example.com.xml  create record api A 203.0.113.20 ttl 300 ? [y/N] y
create  domain/zone/example.com.xml   ok  record[1] create record api A 203.0.113.20 ttl 300
refresh domain/zone/example.com.xml   ok
2 actions, 0 failed, 0 denied
```

## ClouDNS (`cloudns-sub-1234/`)

A GeoDNS zone: `geo` is a location code from `.geodns.xml`; no `geo` is the
default location. Type-specific fields are attributes (`priority`,
`caa-flag`, `redirect-type`). Failover watches record 12.

```shell
$ cd cloudns-sub-1234 && gfs commit --dry-run
create  zone/example.com.xml   would run  create record api A 198.51.100.20 ttl 60 geo NAM  [ask]
dry run: 0 actions, 0 failed, 1 denied
$ gfs commit --allow dns
create  zone/example.com.xml   ok  record[1] id=499153700
1 action, 0 failed, 0 denied
```

Details: `gfs help dns`.
