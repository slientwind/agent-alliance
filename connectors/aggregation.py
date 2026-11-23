"""Compute a pinned set of CSV sources and verify the exported workbook."""

import base64
import csv
import io
import math
import unicodedata
import re
from collections import defaultdict
from decimal import Decimal

import duckdb
from openpyxl import Workbook, load_workbook
from openpyxl.cell.cell import ILLEGAL_CHARACTERS_RE
from openpyxl.styles import Alignment, Font
from openpyxl.utils import get_column_letter

MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"


class InputProblem(ValueError):
    """A deterministic rejection of the input, before a file is issued."""


class MissingColumns(InputProblem):
    def __init__(self, message, issues):
        super().__init__(message)
        self.issues = issues


def csv_records(source, position, group, amount, condition, wanted, unit_column):
    prefix = f"第 {position} 份资料"
    if source.get("version") is not None:
        name = " ".join(source["name"].splitlines())
        prefix += f"《{name}》v{source['version']}"
    reader = csv.reader(
        io.StringIO(source["text"].lstrip("\ufeff"), newline=""), strict=True
    )
    records = []
    scanned = 0
    units = set()
    try:
        header = next(reader, [])
        if not header or len(set(header)) != len(header):
            raise InputProblem(prefix + "的表头为空或存在重复列名")
        if not unit_column:
            candidates = [
                h
                for h in header
                if h.strip().casefold()
                in {"currency", "currency_code", "币种", "货币代码"}
            ]
            if len(candidates) > 1:
                raise InputProblem(
                    prefix + "有多个币种列，请在工具修订中指定金额单位列"
                )
            unit_column = candidates[0] if candidates else None
        required = [col for col in (group, amount, condition, unit_column) if col]
        mapping = source.get("columnMapping") or {}
        if any(
            target not in required or target in header or original not in header
            for target, original in mapping.items()
        ):
            raise InputProblem(prefix + "的字段对应与本版表头不一致")
        columns = {col: mapping.get(col, col) for col in required}
        if len(set(columns.values())) != len(columns):
            raise InputProblem(prefix + "的多个不同字段指向同一列")
        missing = [col for col, original in columns.items() if original not in header]
        if missing:
            message = (
                prefix + "缺少配置的列：" + "、".join(name[:80] for name in missing)
            )
            choices = [
                col for col in header if col.strip() and col not in columns.values()
            ]
            if (
                source.get("id")
                and source.get("version")
                and len(choices) >= len(missing)
                and len(choices) <= 128
                and all(len(col) <= 200 for col in choices + missing)
            ):
                issue = {
                    "source": source["id"],
                    "version": source["version"],
                    "name": source["name"],
                    "missing": missing,
                    "roles": {
                        name: role
                        for name, role in (
                            (group, "分组"),
                            (amount, "金额"),
                            (condition, "筛选"),
                            (unit_column, "金额单位"),
                        )
                        if name in missing
                    },
                    "choices": choices,
                }
                raise MissingColumns(message, [issue])
            raise InputProblem(message)
        key, value = header.index(columns[group]), header.index(columns[amount])
        test = header.index(columns[condition]) if condition else None
        unit_index = header.index(columns[unit_column]) if unit_column else None
        for row in reader:
            if not row:
                continue
            scanned += 1
            if scanned > 10000:
                raise InputProblem(prefix + "超过 10000 行")
            if len(row) != len(header):
                raise InputProblem(f"{prefix}第 {reader.line_num} 行的列数与表头不一致")
            if test is not None and row[test] != wanted:
                continue
            if unit_index is not None:
                unit = row[unit_index].strip()
                if not unit:
                    raise InputProblem(f"{prefix}第 {reader.line_num} 行缺少金额单位")
                units.add(unit)
                if len(units) > 1:
                    raise InputProblem(prefix + "含多种金额单位，请先统一口径")
            raw = row[value].strip()
            if not re.fullmatch(r"-?[0-9]{1,14}(\.[0-9]{1,2})?", raw):
                raise InputProblem(
                    f"{prefix}第 {reader.line_num} 行的金额无效，最多保留两位小数"
                )
            if not row[key].strip():
                raise InputProblem(f"{prefix}第 {reader.line_num} 行缺少分组值")
            records.append((row[key], Decimal(raw)))
    except csv.Error as error:
        raise InputProblem(prefix + "的 CSV 引号或字段格式无效") from error
    return scanned, records, next(iter(units), None)


def write_row(sheet, index, row):
    # Explicit string types preserve identifiers and labels that begin with '='.
    for column, value in enumerate(row, 1):
        if isinstance(value, str) and (
            len(value) > 32767 or ILLEGAL_CHARACTERS_RE.search(value)
        ):
            raise InputProblem("表格文字超出 Excel 单元格长度或含不支持的控制字符")
        cell = sheet.cell(index, column, value)
        if isinstance(value, str):
            cell.data_type = "s"
        elif isinstance(value, Decimal):
            if abs(value) >= Decimal("10000000000000"):
                cell.value = format(value, ".2f")
            else:
                cell.number_format = "0.00"


def wrap_audit_row(sheet, cells):
    # Some spreadsheet renderers keep the default height even when wrapping is enabled.
    lines = 1
    for cell in cells:
        cell.alignment = Alignment(wrap_text=True, vertical="top")
        width = sheet.column_dimensions[cell.column_letter].width
        available = max(1, int(width * 0.82))
        height = 0
        for line in str(cell.value).splitlines() or [""]:
            units = sum(
                2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
                for ch in line
            )
            height += max(1, math.ceil(units / available))
        lines = max(lines, height)
    if lines > 1:
        sheet.row_dimensions[cells[0].row].height = min(409, 17 * (lines + 1))


def check_sheet(sheet, expected):
    actual = sheet.values
    for row in expected:
        observed = next(actual, None)
        if observed is None or len(row) != len(observed):
            raise ValueError("导出行或列不完整")
        for value, saved in zip(row, observed):
            if isinstance(value, Decimal):
                if saved is None or Decimal(str(saved)) != value:
                    raise ValueError("导出金额不一致")
            elif value != saved:
                raise ValueError("导出内容不一致")
    if next(actual, None) is not None:
        raise ValueError("导出文件包含额外行")


def aggregate(sources, text, config):
    if not sources:
        if not text:
            raise InputProblem("请提供含表头的 CSV")
        sources = [{"name": "本次输入", "version": None, "text": text, "id": None}]
    elif any(not s["name"].lower().endswith(".csv") for s in sources):
        raise InputProblem(
            "分组汇总要求每份选定资料都是 CSV，请将表格导出为 CSV 后重新选择"
        )
    group, amount = config["group"], config["amount"]
    condition = config.get("filterColumn") or ""
    wanted = config.get("filterValue") or ""
    records, coverage = [], []
    expected = defaultdict(Decimal)
    source_units = set()
    seen_ids = set()
    missing_columns = []
    for position, source in enumerate(sources, 1):
        identity = source.get("id")
        if identity and identity in seen_ids:
            raise InputProblem("选定资料中有重复的资料标识，请核对所选版本")
        seen_ids.add(identity)
        try:
            scanned, selected, unit = csv_records(
                source, position, group, amount, condition, wanted, config.get("unitColumn")
            )
        except MissingColumns as problem:
            missing_columns.append(problem)
            continue
        if selected:
            source_units.add(unit)
            if len(source_units) > 1:
                raise InputProblem(
                    "选定资料的金额单位不同或部分缺失，请核对单位后再汇总"
                )
        records.extend(selected)
        subtotal = Decimal("0.00")
        for label, value in selected:
            expected[label] += value
            subtotal += value
        coverage.append(
            [
                source["name"],
                source.get("id"),
                source.get("version"),
                scanned,
                len(selected),
                scanned - len(selected),
                subtotal,
            ]
        )
    if len(missing_columns) == 1:
        raise missing_columns[0]
    if missing_columns:
        issues = [issue for problem in missing_columns for issue in problem.issues]
        raise MissingColumns(f"有 {len(issues)} 份资料需要核对字段", issues)
    if not records:
        raise InputProblem("全部选定资料中没有符合筛选条件的记录")
    with duckdb.connect(
        config={
            "enable_external_access": "false",
            "threads": "1",
            "memory_limit": "96MB",
        }
    ) as db:
        db.execute("create table input_data(label varchar, amount decimal(18,2))")
        labels, amounts = zip(*records)
        db.execute(
            "insert into input_data select unnest(?::varchar[]), unnest(?::decimal(18,2)[])",
            [list(labels), list(amounts)],
        )
        grouped = db.execute(
            "select label,sum(amount) from input_data group by label order by label"
        ).fetchall()
        total = db.execute("select sum(amount) from input_data").fetchone()[0]
    if dict(grouped) != expected or total != sum(row[-1] for row in coverage):
        raise ValueError("来源小计与分组汇总不一致")
    summary = [[group, amount], *grouped, ["合计", total]]
    details = [
        ["资料", "资料标识", "版本", "读取行数", "纳入行数", "排除行数", "纳入金额"],
        *coverage,
    ]
    unit = next(iter(source_units)) or "未提供单位列，需核对同一口径"
    criteria = [
        ["项目", "设置"],
        ["分组列", group],
        ["金额列", amount],
        ["金额单位", unit],
        ["筛选列", condition or "全部记录"],
        ["保留值", wanted if condition and wanted else None],
    ]
    book = Workbook()
    book.active.title = "汇总"
    sheets = [
        (book.active, summary, (32, 26)),
        (book.create_sheet("来源核对"), details, (28, 42, 8, 12, 12, 12, 26)),
        (book.create_sheet("计算口径"), criteria, (16, 45)),
    ]
    mappings = [
        [source["name"], source["id"], source["version"], target, original]
        for source in sources
        for target, original in sorted((source.get("columnMapping") or {}).items())
    ]
    mapping_rows = [["资料", "资料标识", "版本", "所需字段", "采用的原表列"], *mappings]
    if mappings:
        sheets.append(
            (book.create_sheet("字段对应"), mapping_rows, (32, 42, 8, 33, 33))
        )
    for sheet, rows, widths in sheets:
        for index, row in enumerate(rows, 1):
            write_row(sheet, index, row)
        for column, width in enumerate(widths, 1):
            sheet.column_dimensions[get_column_letter(column)].width = width
        for cell in sheet[1]:
            cell.font = Font(bold=True)
        sheet.freeze_panes = "A2"
        sheet.print_title_rows = "1:1"
        sheet.page_setup.orientation = "landscape"
        sheet.page_setup.paperSize = sheet.PAPERSIZE_A4
        sheet.sheet_properties.pageSetUpPr.fitToPage = True
        sheet.page_setup.fitToWidth = 1
        sheet.page_setup.fitToHeight = 0
    for cell in book.active[len(summary)]:
        cell.font = Font(bold=True)
    for row in book["来源核对"].iter_rows(min_row=2, max_col=1):
        wrap_audit_row(book["来源核对"], row)
    if mappings:
        for row in book["字段对应"].iter_rows(min_row=2):
            wrap_audit_row(book["字段对应"], (row[0], row[3], row[4]))
    output = io.BytesIO()
    book.save(output)
    output.seek(0)
    verification = load_workbook(output, read_only=True, data_only=True)
    try:
        check_sheet(verification["汇总"], summary)
        check_sheet(verification["来源核对"], details)
        check_sheet(verification["计算口径"], criteria)
        if mappings:
            check_sheet(verification["字段对应"], mapping_rows)
    finally:
        verification.close()
    body = "\n".join(f"{label} · {value}" for label, value in summary)
    body += f"\n已核对 {len(coverage)} 份输入，共 {sum(r[3] for r in coverage)} 行，纳入 {len(records)} 行。"
    body += f"\n金额单位：{unit}"
    if condition:
        body += f"\n筛选条件：{condition} = {wanted}"
    return {
        "text": body,
        "file": {
            "name": "deliverable.xlsx",
            "mime": MIME,
            "data": base64.b64encode(output.getvalue()).decode("ascii"),
        },
    }
