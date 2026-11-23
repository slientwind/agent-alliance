export const outputOperations: Record<string, string> = {
  png: "PNG 图像 · 可预览、下载及外部修订回传，Agent 可按文字反馈重新生成整张图片",
  text: "文本 · 可在线修改或向负责 Agent 提交改稿意见",
  extract: "摘录文本 · 可在线修改",
  aggregate:
    "XLSX 汇总表 · 合并所有选定 CSV，附各来源版本及小计，可预览或外部修订回传",
  docx: "DOCX 文档 · 可预览，外部编辑后回传，或按修订正文重新生成",
  xlsx: "XLSX 工作簿 · 可预览，外部编辑后回传，或按修订正文重新生成",
  pptx: "PowerPoint 课件（PPTX） · 可预览，外部编辑后回传，或按修订正文重新生成",
};

export const fileFormats: Record<string, string> = {
  docx: "Word 文档（DOCX）",
  xlsx: "Excel 工作簿（XLSX）",
  pptx: "PowerPoint 演示文稿（PPTX）",
  png: "PNG 图片",
};

export function fileFormat(operation?: string) {
  if (operation === "extract") return "text";
  return operation === "aggregate" ? "xlsx" : operation;
}

export const plannedOutputs: Record<string, string> = {
  ...fileFormats,
  text: "文本答复",
};

// Interpret a standalone format name as an output filter. A longer request can
// also name input files, so it stays a text search.
type SearchFormat = "docx" | "xlsx" | "pptx" | "png" | "text";
export const formatSearchAliases: ReadonlyMap<string, SearchFormat> = new Map([
  ["word", "docx"],
  ["docx", "docx"],
  ["excel", "xlsx"],
  ["xlsx", "xlsx"],
  ["powerpoint", "pptx"],
  ["ppt", "pptx"],
  ["pptx", "pptx"],
  ["png", "png"],
  ["文本", "text"],
  ["纯文本", "text"],
  ["text", "text"],
]);
