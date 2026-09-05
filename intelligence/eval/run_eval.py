"""Runs the income-classification evaluation suite and prints real,
measured accuracy — the actual deliverable of README Section 8a's
"Categorization Evaluation Suite" add-on: back a Claude-powered feature's
accuracy claim with numbers, not vibes.

Run from the repo root (same convention as main.py / mcp_server.py):

    intelligence/venv/bin/python -m intelligence.eval.run_eval

Deliberately calls enrich_transaction directly against LabeledExample
objects rather than real Transaction rows pulled from Postgres — Python's
duck typing means enrich_transaction doesn't care that a LabeledExample
isn't a SQLAlchemy model, only that it has the same four attributes
(merchant_name, category, amount, transaction_date) the prompt reads. No
database, no Kafka, no other service needs to be running to use this.
"""
from .. import config
from ..enrichment import enrich_transaction
from ..risk_engine import RiskAssessment
from .labeled_examples import EXAMPLES, LabeledExample

# A neutral, empty risk assessment — this suite measures income
# classification accuracy specifically, not risk-summary quality, so every
# example is run as if it triggered no rules at all.
_NEUTRAL_ASSESSMENT = RiskAssessment(score=0, level="LOW", reasons=[])


def _classify(example: LabeledExample) -> str | None:
    result = enrich_transaction(example, _NEUTRAL_ASSESSMENT)
    return result.income_classification


def main() -> None:
    if not config.ANTHROPIC_API_KEY:
        print(
            "ANTHROPIC_API_KEY isn't set in intelligence/.env — enrich_transaction\n"
            "would just skip every example and report 0% accuracy, which isn't a real\n"
            "measurement. Add a key first, then run this again."
        )
        return

    rows = []
    for example in EXAMPLES:
        predicted = _classify(example)
        correct = predicted == example.expected_income_classification
        rows.append((example, predicted, correct))

    print(f"{'source':<10} {'merchant':<26} {'expected':<14} {'predicted':<14} {'ok'}")
    print("-" * 80)
    for example, predicted, correct in rows:
        mark = "yes" if correct else "NO"
        print(
            f"{example.source:<10} {example.merchant_name:<26} "
            f"{example.expected_income_classification:<14} {str(predicted):<14} {mark}"
        )

    def accuracy(subset: list[tuple[LabeledExample, str | None, bool]]) -> str:
        if not subset:
            return "n/a (no examples)"
        correct_count = sum(1 for _, _, c in subset if c)
        return f"{correct_count}/{len(subset)} ({100 * correct_count / len(subset):.0f}%)"

    real_rows = [r for r in rows if r[0].source == "real"]
    synthetic_rows = [r for r in rows if r[0].source == "synthetic"]

    print("-" * 80)
    print(f"Overall accuracy:        {accuracy(rows)}")
    print(f"Real Sandbox data only:  {accuracy(real_rows)}  <- the only claim backed by real usage")
    print(f"Synthetic examples only: {accuracy(synthetic_rows)}  <- hand-crafted, covers salary/gig_income")

    print("\nPer-category breakdown:")
    categories = sorted({e.expected_income_classification for e, _, _ in rows})
    for category in categories:
        subset = [r for r in rows if r[0].expected_income_classification == category]
        print(f"  {category:<14} {accuracy(subset)}")


if __name__ == "__main__":
    main()
