# CloudSat coverage discovery

For `3F-RMCP.R05_V0002` and `3S-RMCP.R05_V0002`, the provider walks the
product's THREDDS catalog references and interprets the documented RMCP
filename period as a UTC half-open interval. This includes monthly, seasonal,
annual, and explicitly named month ranges. The product documentation describes
DJF as starting in December of the named year. Catalog failures and unknown
filenames fail the search rather than silently claiming complete results.

For the exact collections `2B-GEOPROF.P1_R05` and
`2B-CLDCLASS.P1_R05`, when an authenticated metadata reader is configured,
the provider searches starting 6001 seconds before the requested start. The
official P1_R05 specifications bound `Profile_time` to [0,6000] seconds, and
the filename gives the first profile to an integer second. The provider verifies
the actual `UTC_start` falls within that filename second, validates every
binary FLOAT32 profile value, and fails the search on invalid metadata. The
start and end describe the observed sample timestamp span: the maximum profile
timestamp plus one nanosecond is the exclusive end. This is a representation
of the last discrete timestamp, not a physical footprint or exposure duration.
The original last timestamp is retained as `last_profile_time` metadata.

Other orbit collections, and these two when no metadata reader is configured,
still search day catalogs by filename start and return no end for those granules.
The inspected 2B-CLDCLASS day catalog provides size and modification date,
not observation end. A file starting before the query can therefore be missed,
including across midnight. Modification time, adjacent file starts, and nominal
orbit periods are not observation coverage. A complete fix needs an authoritative
searchable coverage index or authenticated per-file observation metadata and a
discovery strategy that can find earlier starts. For the two supported
collections the service uses its configured account via the central gateway;
it fetches only the two time fields, not complete scientific files.

## Orbit metadata investigation

An independently published ccplot sample,
`2009037050924_14779_CS_2B-GEOPROF_GRANULE_P_R04_E02.hdf`, was downloaded
in full (34,237,802 bytes; SHA-256
`b7c94678b85e82d606df9981a604ddd447ebaada3283d3112b606a3f945b714d`).
HDF4 Vdata contains 37,082 `Profile_time` values, from 0 to
5932.9599609375 seconds, and `UTC_start` 18564.19921875 seconds from UTC
midnight. Its global `start_time` and `end_time` are integer-second strings
`20090206050924` and `20090206064817`; the latter loses the fractional last
profile time and is not by itself an exact exclusive boundary.

For the DPC `2B-CLDCLASS.P1_R05` file
`2006365231248_03606_CS_2B-CLDCLASS_GRANULE_P1_R05_E02_F00.hdf`, authenticated
OPeNDAP `.dds` reports 37,081 profiles. A constrained `.ascii` response
reports `UTC_start=83568.914` seconds and final `Profile_time=5932.799`
seconds, placing the first and last profile timestamps approximately at
2006-12-31 23:12:48.914 and 2007-01-01 00:51:41.713 UTC. The full-file
transfer timed out after receiving 7,683,785 of 10,918,262 bytes; no full-file
hash or conclusions about complete file contents are claimed for it.
Constrained binary responses for that granule in both CLDCLASS and GEOPROF
P1_R05 contain 37,081 profiles and parse to the exact FLOAT32 timestamps
2006-12-31 23:12:48.9140625 and 2007-01-01 00:51:41.712890625 UTC;
the responses were 148,587 and 148,583 bytes respectively.

The authenticated service path requests the two time fields in an OPeNDAP
binary response, rather than decimal ASCII, preserving the source FLOAT32
values. The bound and sample-timestamp convention above apply only to the two
named collections, not all CloudSat products. The historical handbook describes
profile timestamps as centers of 0.16-second intervals; this implementation
does not use that interval to invent physical coverage boundaries.

Evidence:

- [DPC THREDDS instructions and orbit URL layout](https://www.cloudsat.cira.colostate.edu/order/THREDDS-OpeNDAP-access)
- [CloudSat data product filename definition](https://www.cloudsat.cira.colostate.edu/data-products)
- [2B-CLDCLASS P1_R05 specification](https://www.cloudsat.cira.colostate.edu/cloudsat-static/info/dl/2b-cldclass/2B-CLDCLASS_PDICD.P1_R05.rev1_.pdf)
- [2B-GEOPROF R05 field ranges](https://www.cloudsat.cira.colostate.edu/data-products/2b-geoprof)
- [CloudSat handbook, section 2](https://ccplot.sourceforge.net/pub/resources/CloudSat/CloudSat%20Data%20Users%20Handbook.pdf)
- [Public ccplot GEOPROF sample](https://sourceforge.net/projects/ccplot/files/products/2009037050924_14779_CS_2B-GEOPROF_GRANULE_P_R04_E02.hdf/download)
- [RMCP product specification, sections 2.3 and 2.6](https://www.cloudsat.cira.colostate.edu/cloudsat-static/info/dl/3f-3s-rmcp/3FS-RMCP_PDICD.R05_V0002.rev0__0.pdf)
- [Example orbit day catalog](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/2B-CLDCLASS.P1_R05/2007/001/catalog.xml)
- [3F RMCP product catalog](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/catalog.xml)
- [3F RMCP monthly files](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/monthly/catalog.xml)
- [3S RMCP product catalog](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/catalog.xml)
- [3S RMCP seasonal files](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/seasonal/catalog.xml)
- [3S RMCP annual files](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/annual/catalog.xml)
