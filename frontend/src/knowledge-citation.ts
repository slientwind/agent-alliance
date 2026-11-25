export function citationPath(space: string, doc: string, version: number, ordinal: number) {
  return `spaces/${encodeURIComponent(space)}/knowledge/documents/${encodeURIComponent(doc)}/citations/${version}/${ordinal}`;
}
