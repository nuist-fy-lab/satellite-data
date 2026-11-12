export function sourceFamilyID(sourceID) {
  const id = String(sourceID || '').toLowerCase();
  if (/^goes\d+$/.test(id)) return 'goes';
  if (/^himawari\d+$/.test(id)) return 'himawari';
  if (id === 'iasi-ng') return 'iasi';
  return id;
}

export function downloadProviderFamilyID(sourceID) {
  const id = String(sourceID || '').toLowerCase();
  return id === 'iasi-ng' ? 'iasi' : id;
}
