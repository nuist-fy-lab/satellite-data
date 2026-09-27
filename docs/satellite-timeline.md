# Satellite timeline sources

The year page shows one mission-period row per satellite in the selected source's
catalogue. It is **not** a claim that every day or product is available or
downloaded. A source catalogue's `time_end` is a metadata extent, never an
END-of-mission marker. Only a confirmed end of science observations gets END.

- CloudSat: launched 2006; radar science operations ended 2023-12-20.
  https://science.nasa.gov/mission/cloudsat/
- CALIPSO: launched 2006; science operations ended 2023-08-01.
  https://science.nasa.gov/mission/calipso/
- Terra: MODIS observations began in 2000; Aqua launched in 2002.
  https://science.nasa.gov/blogs/earth-matters/2019/12/18/terra-turns-twenty/
  https://science.nasa.gov/mission/aqua/
- Suomi-NPP: launched 2011; NOAA-20 launched 2017; NOAA-21 launched 2022.
  https://www.nesdis.noaa.gov/about/our-offices/office-of-low-earth-orbit-observations
- GOES-16 through GOES-19: approximate first mission years 2017, 2018, 2022,
  2024. GOES-17's retirement as GOES West in 2023 is **not** an end of mission;
  NOAA lists it as on-orbit storage.
  https://goes-r.noaa.gov/mission/mission.html
  https://goes-r.noaa.gov/mission/history.html
- Himawari-8 started main operation 2015, became standby 2022; Himawari-9
  started main operation 2022. Standby is **not** an end of mission.
  https://www.jma.go.jp/jma/jma-eng/satellite/
- EarthCARE launched in 2024; MTG-I1 launched in 2022. The MTG row represents
  a satellite family in the current provider catalogue.
  https://www.esa.int/Applications/Observing_the_Earth/FutureEO/EarthCARE
  https://www.eumetsat.int/media-invitation-meteosat-third-generation-imager-1-launch

Unlisted satellites use the earliest catalogue `time_start` year as a provisional
start, or show an unknown start when the catalogue has no such metadata. This
never implies an end date. The right edge for open-ended rows is today, not a
promise of continuous observation or up-to-date indexing.
