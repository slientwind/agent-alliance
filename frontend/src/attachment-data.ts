export const pictureExtensions = [".png", ".jpg", ".jpeg"];
export function isPictureName(name: string) {
  return /\.(png|jpe?g)$/i.test(name);
}

export function encodeAttachment(file: File): Promise<{ name: string; data: string }> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error(`无法读取 ${file.name}，请重新选择。`));
    reader.onload = () => {
      const encoded = String(reader.result);
      resolve({ name: file.name, data: encoded.slice(encoded.indexOf(",") + 1) });
    };
    reader.readAsDataURL(file);
  });
}
