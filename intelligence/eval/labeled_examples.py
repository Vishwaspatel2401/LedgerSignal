"""Ground-truth labels for the income-classification evaluation suite
(README Section 8a: "Categorization Evaluation Suite").

Two sources, kept explicitly distinct — never blur them into one number
without saying so:

- REAL examples: the two distinct inflow patterns that actually exist in
  this project's Sandbox data (checked directly against Postgres — 12 raw
  rows, but only 2 unique merchant/category/amount combinations, repeated
  across linked items and months). Testing the same input 6 times adds
  count, not signal, so each real pattern appears here once.
- SYNTHETIC examples: hand-crafted, because the real Sandbox data has ZERO
  examples of "salary" or "gig_income" — Plaid's Sandbox test accounts
  simply don't generate that kind of transaction. Without these, 2 of the
  5 possible classifications could never be measured at all. Labeled
  clearly as synthetic so nobody mistakes a hand-crafted example for
  something that actually happened in Sandbox.

Ground truth for the real "INTRST PYMNT" transaction is "other", not
"transfer" — worth a note since it's a genuine judgment call: interest
credited by the bank isn't a transfer between accounts or people (which is
what "transfer" means for the other examples here), so it doesn't fit that
category any better than "other" does. Revisit this label if you disagree —
it's a human call, not a fact.
"""
from dataclasses import dataclass


@dataclass
class LabeledExample:
    source: str  # "real" or "synthetic" — never omit this
    plaid_transaction_id: str
    merchant_name: str
    category: str
    amount: float  # Plaid convention: negative = inflow
    transaction_date: str
    expected_income_classification: str  # ground truth


EXAMPLES: list[LabeledExample] = [
    # --- real (from this project's actual Sandbox data) ---
    LabeledExample(
        source="real",
        plaid_transaction_id="eval-real-united-airlines",
        merchant_name="United Airlines",
        category="TRAVEL",
        amount=-500.00,
        transaction_date="2026-08-10",
        expected_income_classification="refund",
    ),
    LabeledExample(
        source="real",
        plaid_transaction_id="eval-real-intrst-pymnt",
        merchant_name="INTRST PYMNT",
        category="TRANSFER_IN",
        amount=-4.22,
        transaction_date="2026-08-07",
        expected_income_classification="other",
    ),
    # --- synthetic: salary (zero real examples exist to test this at all) ---
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-payroll-1",
        merchant_name="ACME CORP PAYROLL",
        category="INCOME",
        amount=-2400.00,
        transaction_date="2026-07-15",
        expected_income_classification="salary",
    ),
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-payroll-2",
        merchant_name="GLOBEX INC DIRECT DEP",
        category="INCOME",
        amount=-3100.00,
        transaction_date="2026-07-01",
        expected_income_classification="salary",
    ),
    # --- synthetic: gig income (zero real examples exist to test this at all) ---
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-gig-1",
        merchant_name="UBER EARNINGS DEPOSIT",
        category="INCOME",
        amount=-187.32,
        transaction_date="2026-07-20",
        expected_income_classification="gig_income",
    ),
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-gig-2",
        merchant_name="DOORDASH PAY",
        category="INCOME",
        amount=-96.14,
        transaction_date="2026-07-18",
        expected_income_classification="gig_income",
    ),
    # --- synthetic: transfer ---
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-transfer-1",
        merchant_name="TRANSFER FROM SAVINGS",
        category="TRANSFER_IN",
        amount=-300.00,
        transaction_date="2026-07-22",
        expected_income_classification="transfer",
    ),
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-transfer-2",
        merchant_name="VENMO CASHOUT",
        category="TRANSFER_IN",
        amount=-45.00,
        transaction_date="2026-07-25",
        expected_income_classification="transfer",
    ),
    # --- synthetic: refund ---
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-refund-1",
        merchant_name="AMAZON REFUND",
        category="GENERAL_MERCHANDISE",
        amount=-34.99,
        transaction_date="2026-07-28",
        expected_income_classification="refund",
    ),
    # --- synthetic: other ---
    LabeledExample(
        source="synthetic",
        plaid_transaction_id="eval-synth-other-1",
        merchant_name="MISC CREDIT ADJUSTMENT",
        category="GENERAL_SERVICES",
        amount=-12.00,
        transaction_date="2026-07-30",
        expected_income_classification="other",
    ),
]
