"""Generate the synthetic evidence pack for Argus lets-quant-report.v1."""
import csv
from datetime import date, timedelta
from hashlib import sha256
import json


def stage(root):
    dates = [
        date(2024, 1, 1) + timedelta(days=offset)
        for offset in range(362)
        if (date(2024, 1, 1) + timedelta(days=offset)).weekday() < 5
    ]
    cases = (
        ("configured-costs", "Configured costs", 110, 6, {}),
        ("high-friction", "High friction", 108, 4,
         dict(commission_rate=0.005, minimum_commission=2, sell_tax_rate=0.001, slippage_bps=12)),
        ("delayed-execution", "Delayed execution", 105, 3,
         dict(execution_delay_trading_days=1, slippage_bps=8)),
    )
    facts = dict(experiment_input_id=sha256(b"synthetic-lets-quant-input").hexdigest(),
                 case_count=len(cases), cases={}, execution_scenarios=[])
    rows = []
    for name, label, final, trades, overrides in cases:
        output = root / f"{name}.csv"
        with output.open("w", newline="") as stream:
            writer = csv.writer(stream, lineterminator="\n")
            writer.writerow(("date", "nav", "cash", "drawdown", "risk_frozen", "positions"))
            writer.writerows((day, nav, 100, 0, "false", 0) for day, nav in
                             zip(dates, [100] * (len(dates) - 1) + [final], strict=True))
        facts["cases"][name] = dict(label=label, filled_trade_count=trades,
                                    source_sha256=sha256(output.read_bytes()).hexdigest())
        facts["execution_scenarios"].append(dict(name=name, **overrides))
        rows.append(f"| {label} | {final - 100}.000% | 0.000% | {trades} |")
    facts["result_sha256"] = sha256(
        "".join(case["source_sha256"] for case in facts["cases"].values()).encode()
    ).hexdigest()
    (root / "facts.json").write_text(json.dumps(facts, indent=2) + "\n", encoding="utf-8")
    metrics = "\n".join(["| Scenario | Return | Drawdown | Fills |", "| --- | --- | --- | --- |", *rows])
    summary = "# Synthetic lets-quant report\n\n" + metrics
    good = summary + (
        "\n\n## Frozen evidence\n\n"
        f"Experiment input {facts['experiment_input_id']}\n\nResult {facts['result_sha256']}\n\n"
        "高摩擦和延迟执行分别低 200.00 和 500.00 个基点。\n\n"
        "| Scenario | Execution overrides |\n| --- | --- |\n"
        "| High friction | `commission_rate=0.005` `minimum_commission=2` `sell_tax_rate=0.001` `slippage_bps=12` |\n"
        "| Delayed execution | `execution_delay_trading_days=1` `slippage_bps=8` |"
    )
    bad = good.replace(f"Experiment input {facts['experiment_input_id']}", "[Evidence](delivery-manifest.json)")
    bad = bad.replace("500.00 个基点", "450.00 个基点")
    for version, body in ((1, bad), (2, good)):
        (root / f"report-v{version}.md").write_text(body + "\n", encoding="utf-8")
