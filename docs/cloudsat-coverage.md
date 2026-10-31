# CloudSat coverage discovery

For `3F-RMCP.R05_V0002` and `3S-RMCP.R05_V0002`, the provider walks the
product's THREDDS catalog references and interprets the documented RMCP
filename period as a UTC half-open interval. This includes monthly, seasonal,
annual, and explicitly named month ranges. The product documentation describes
DJF as starting in December of the named year. Catalog failures and unknown
filenames fail the search rather than silently claiming complete results.

This does not fix orbit-granule leading-boundary discovery. The provider still
searches day catalogs by filename start and returns no end for those granules.
The inspected 2B-CLDCLASS day catalog provides size and modification date,
not observation end. A file starting before the query can therefore be missed,
including across midnight. Modification time, adjacent file starts, and nominal
orbit periods are not observation coverage. A complete fix needs an authoritative
searchable coverage index or authenticated per-file observation metadata and a
discovery strategy that can find earlier starts. The DPC documents authenticated
OPeNDAP access; this implementation does not fetch scientific data or use account
credentials during search.

Evidence:

- [DPC THREDDS instructions and orbit URL layout](https://www.cloudsat.cira.colostate.edu/order/THREDDS-OpeNDAP-access)
- [RMCP product specification, sections 2.3 and 2.6](https://www.cloudsat.cira.colostate.edu/cloudsat-static/info/dl/3f-3s-rmcp/3FS-RMCP_PDICD.R05_V0002.rev0__0.pdf)
- [Example orbit day catalog](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/2B-CLDCLASS.P1_R05/2007/001/catalog.xml)
- [3F RMCP product catalog](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/catalog.xml)
- [3F RMCP monthly files](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3F-RMCP.R05_V0002/monthly/catalog.xml)
- [3S RMCP product catalog](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/catalog.xml)
- [3S RMCP seasonal files](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/seasonal/catalog.xml)
- [3S RMCP annual files](https://dpc-thredds.cira.colostate.edu/thredds/catalog/cloudsat-data/3S-RMCP.R05_V0002/annual/catalog.xml)
