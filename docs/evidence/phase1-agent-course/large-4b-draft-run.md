```python
# 生成PPTX文件的代码

from slides import slides

# 初始化PPTX文件
pptx = slides.make_pptx()

# 第一页：已知规格
slide = pptx.add_slide()
slide.title = "已知规格"

# 添加产品编号、容量、颜色和包装内含物
slide.add_text("产品编号：CW-01", 1, 1)
slide.add_text("容量：480 mL", 1, 2)
slide.add_text("颜色：海蓝色", 1, 3)
slide.add_text("包装内含物：杯和说明卡", 1, 4)

# 第二页：待补资料
slide = pptx.add_slide()
slide.title = "待补资料"

# 添加零售价和保温时长两项的资料状况
slide.add_text("零售价：资料尚无", 1, 1)
slide.add_text("保温时长：尚无测量资料", 1, 2)

# 指出交付前由谁核对规格
slide.add_text("交付前由产品审核人核对规格", 1, 3)

# 保存PPTX文件
pptx.save("内部评审简报.pptx")
```
