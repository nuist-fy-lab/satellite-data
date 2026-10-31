# Himawari ISatSS sample evidence

The exact local collection keys `himawari8:AHI-L2-FLDK-ISatSS` and
`himawari9:AHI-L2-FLDK-ISatSS` contain sectorized full-disk imagery in the
samples below. The bucket paths, file metadata and variables establish the
mapping; the local `L2` prefix alone does not establish the content. NOAA's
[Himawari-8 notice](https://www.ospo.noaa.gov/data/messages/2021/03/MSG_20210330_1314.html)
and [Himawari-9 notice](https://ospo.noaa.gov/data/messages/2022/11/MSG_20221121_2012.html)
describe HFD-named full-disk cloud/moisture imagery, although their directory
names differ from the sampled bucket paths.

The scientific subtype is `sectorized_imagery` (分块影像): each sampled file
is one channel and one tile, not a multi-channel file.

Samples were retrieved anonymously on 2026-10-31 from the indicated public
bucket paths, into temporary storage only. SHA-256 covers the complete file.

| Bucket and object | Bytes | SHA-256 |
| --- | ---: | --- |
| `noaa-himawari8/AHI-L2-FLDK-ISatSS/2019/07/01/0120/OR_HFD-010-B11-M1C02-T071_GH8_s2019182012000_c2020183141847.nc` | 205249 | `297e0310ee137048f8d01acd7274bc660017795e121d161690cbdcdc9f3cfe1b` |
| `noaa-himawari9/AHI-L2-FLDK-ISatSS/2022/12/12/1830/OR_HFD-005-B11-M1C03-T007_GH9_s20223461830000_c20223461838330.nc` | 2538245 | `6119bf3e52b113fe0bf1c701f253edb3130b97f7ae65c26706b9772db9042441` |
| `noaa-himawari9/AHI-L2-FLDK-ISatSS/2022/12/12/1830/OR_HFD-020-B11-M1C16-T055_GH9_s20223461830000_c20223461839190.nc` | 298676 | `4aba26ee9706e01339d079b56e92bfab8160bd913425030937f68e28ddf0c1fb` |

The H8 title is `Sectorized Cloud and Moisture Full Disk Imagery`; both H9
titles are `Sectorized Full Disk Imagery`. All three record `source_scene=Full
Disk`, and the `Sectorized_CMI` variable. H8 C02 and H9 C03 describe
`toa_bidirectional_reflectance` with units `1`; H9 C16 describes
`brightness_temperature` in `kelvin`. Their `start_date_time` values are
`2019182012000` and `2022346183000`, respectively. The samples contain no
end-time attribute or time-coordinate variable. `periodicity=10` is present,
but is not evidence of an exact coverage end or a recurring selection axis.

All three report `ICD_version=SE-08_7034704_GS_AWIPS_Ext_ICD_RevB.3`;
this is an interface specification identifier, not an algorithm or input
processing version. No algorithm or input version was established from these
samples. The evidence does not establish that every historical file has the
same metadata or that any particular historical time is available.
