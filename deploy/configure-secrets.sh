#!/usr/bin/env bash
set -euo pipefail

umask 077
config_dir="${HOME}/.config/satellite-data-downloader"
target="${config_dir}/secrets.env"
mkdir -p "${config_dir}"
temp="$(mktemp "${config_dir}/secrets.env.XXXXXX")"
trap 'rm -f "${temp}"' EXIT

prompt_secret() {
  local label="$1"
  local var="$2"
  local value
  printf '%s (leave empty to skip): ' "${label}" >&2
  IFS= read -r -s value
  printf '\n' >&2
  if [[ -z "${value}" ]]; then
    return 0
  fi
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s="%s"\n' "${var}" "${value}" >>"${temp}"
}

echo "Satellite downloader credential setup"
echo "Values are written directly to ${target} with mode 0600 and are not echoed."
echo

prompt_secret "LAADS download token (MODIS + VIIRS)" SATDOWN_LAADS_TOKEN
prompt_secret "NASA Earthdata bearer token (CALIPSO)" SATDOWN_EARTHDATA_TOKEN
prompt_secret "ESA MAAP access token (EarthCARE)" SATDOWN_EARTHCARE_TOKEN
prompt_secret "EUMETSAT Consumer Key (MTG)" SATDOWN_EUMETSAT_CONSUMER_KEY
prompt_secret "EUMETSAT Consumer Secret (MTG)" SATDOWN_EUMETSAT_CONSUMER_SECRET
prompt_secret "CloudSat DPC username" SATDOWN_CLOUDSAT_USERNAME
prompt_secret "CloudSat DPC password" SATDOWN_CLOUDSAT_PASSWORD

chmod 600 "${temp}"
mv -f "${temp}" "${target}"
trap - EXIT
printf 'Wrote %s with mode 0600. Restart satellite-data-downloader.service after changes.\n' "${target}"
