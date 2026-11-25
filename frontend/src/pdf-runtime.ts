import { getDocument, GlobalWorkerOptions } from "pdfjs-dist";
import worker from "pdfjs-dist/build/pdf.worker.min.mjs?url";

export function openPDF(data: ArrayBuffer) {
  GlobalWorkerOptions.workerSrc = worker;
  return getDocument({ data, isEvalSupported: false, enableXfa: false, useSystemFonts: true, maxImageSize: 16000000 });
}
