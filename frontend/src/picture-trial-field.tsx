import { pictureExtensions } from "./attachment-data";

export function PictureTrialField({ onChange }: { onChange?: () => void }) {
  return (
    <label className="picture-trial-field">
      试运行图片
      <input type="file" name="picture" accept={pictureExtensions.join(",")} onChange={onChange} required />
      <small>请使用获准的样例。PNG 或 JPEG，单张最多 3 MiB。</small>
    </label>
  );
}
