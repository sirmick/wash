// saveDownload drops bytes into the browser's downloads under name via a
// synthetic anchor click — the save half of @wash/fs-client's
// createDownloads. testid lands on the anchor so an e2e can assert the
// trigger fired without depending on the OS download chrome.
export function saveDownload(name: string, chunks: Uint8Array[], testid: string): void {
  const blob = new Blob(chunks as BlobPart[], { type: 'application/octet-stream' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.dataset.testid = testid;
  a.dataset.name = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Revoke after a tick so the navigation/save has picked up the URL.
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
