---
name: token-compression
description: >
  Maximum token compression by substituting natural language with mathematical
  notation, code, physical formulas, and chemical notation (IUPAC, SMILES,
  reaction equations, periodic table symbols). ALWAYS use this skill when asked
  to reason, explain, analyze, calculate, model, design, compare, or
  optimize anything. Trigger words: "reason", "think", "explain", "analyze",
  "calculate", "model", "design", "how does X work", "compare", "optimize",
  "chemical", "reaction", "molecule", "compound".
  Golden rule: if a natural language phrase has a compact formal equivalent → USE THE FORMAL.
---

# Token Compression Skill v4

## Core principle: two budgets, never conflate

```python
budget = {
    "thinking": { "compress": False },  # free CoT — reasoning quality ∝ tokens
    "output":   { "compress": True  },  # compress aggressively — all arsenals active
}
# Wei et al. 2022: quality ∝ N_thinking → NEVER compress thinking
# Output budget: math + code + physics + chemistry → target 2-5× reduction [EMPIRICAL]
```

---

## ARSENAL 1 — Mathematics & Logic

```
Relations:   y ∝ x  |  dy/dx > 0  |  X ⟹ Y  |  A ⟺ B  |  A ≡ B  |  A ≈ B
Quantifiers: ∀x ∈ S: P(x)  |  ∃x: P(x)  |  ∴ Q  |  ∵ P
Sets:        ∈ ∉ ⊂ ⊆ ∪ ∩ ∅ ℝ ℕ ℤ ℂ
Calculus:    ∂f/∂x  |  ∫f dx  |  ∑ᵢ  |  ∏ᵢ  |  ∇f  |  lim_{x→∞}
Info/ML:     H(X) = -Σ pᵢ log₂pᵢ  |  I(X;Y)  |  D_KL(P‖Q)  |  𝔼[X]  |  ∇L
Complexity:  O(1) < O(log n) < O(n) < O(n log n) < O(n²) < O(2ⁿ)
```

---

## ARSENAL 2 — Physics

```
Mechanics:      F = ma  |  E = ½mv²  |  p = mv
Relativity:     E = mc²
Thermodynamics: S = k_B ln Ω  |  ΔG = ΔH - TΔS  |  dS/dt ≥ 0
Quantum:        E = hf = ℏω  |  ΔxΔp ≥ ℏ/2  |  ĤΨ = EΨ
EM:             F = qE + qv×B  |  c = 1/√(ε₀μ₀)

Constants (CODATA 2022):
  c = 299792458 m/s     h = 6.62607015×10⁻³⁴ J·s    k_B = 1.380649×10⁻²³ J/K
  e = 1.602176634×10⁻¹⁹ C   N_A = 6.02214076×10²³ mol⁻¹
```

---

## ARSENAL 3 — Chemistry (PRIMARY CONTRIBUTION)

### Key insight: notation IS reasoning
A model writing `ΔG = ΔH - TΔS` reasons like a thermodynamicist.
A model writing `CC(=O)Oc1ccccc1C(=O)O` operates in biochemist semantic space.
Formal notation activates expert reasoning patterns, not just compresses tokens.

### 3.1 Periodic table — element symbols

```
H He Li Be B C N O F Ne Na Mg Al Si P S Cl Ar
K Ca Sc Ti V Cr Mn Fe Co Ni Cu Zn Ga Ge As Se Br Kr
Rb Sr Y Zr Nb Mo Tc Ru Rh Pd Ag Cd In Sn Sb Te I Xe
Cs Ba La Hf Ta W Re Os Ir Pt Au Hg Tl Pb Bi Po At Rn

Key: hydrogen→H  carbon→C  nitrogen→N  oxygen→O  sodium→Na
     iron→Fe  gold→Au  silver→Ag  copper→Cu  lead→Pb  mercury→Hg
```

### 3.2 Common molecules — IUPAC

```
H₂O  CO₂  O₂  N₂  HCl  H₂SO₄  NaOH  NaCl  NH₃
CH₄  C₂H₅OH  C₆H₆  C₆H₁₂O₆  C₁₂H₂₂O₁₁
ATP: C₁₀H₁₆N₅O₁₃P₃   Aspirin: C₉H₈O₄   Caffeine: C₈H₁₀N₄O₂
Amino acids: G A V L I P F W M S T C Y H D E N Q K R
```

### 3.3 SMILES notation — maximum structural compression

```
water:     O
ethanol:   CCO
benzene:   c1ccccc1
aspirin:   CC(=O)Oc1ccccc1C(=O)O
caffeine:  Cn1c(=O)c2c(ncn2C)n(c1=O)C
glucose:   OC[C@H]1OC(O)[C@H](O)[C@@H](O)[C@@H]1O
dopamine:  NCCc1ccc(O)c(O)c1
ATP:       Nc1ncnc2c1ncn2[C@@H]1O[C@H](COP(=O)(O)OP(=O)(O)OP(=O)(O)O)[C@@H](O)[C@H]1O

# SMILES >> verbal description for molecular structure
```

### 3.4 Reaction equations

```
→  irreversible  |  ⇌  equilibrium  |  ⇒  strongly favored
(s) solid  (l) liquid  (g) gas  (aq) aqueous
↑ gas product  |  ↓ precipitate

Measured savings (cl100k_base, n=10):
  pH = -log[H⁺]           [5.0×]
  PV = nRT                 [4.5×]
  ΔG = ΔH - TΔS           [3.0×]
  C₆H₁₂O₆ + 6O₂ → ...    [1.6×]
  Average: 2.7×
```

### 3.5 Chemical thermodynamics & kinetics

```
ΔG = ΔH - TΔS  |  ΔG° = -RT ln K  |  K = [products]/[reactants]
r = k[A]^m[B]^n  |  k = Ae^(-Ea/RT)  (Arrhenius)
pH + pOH = 14  |  pH = pKa + log([A⁻]/[HA])  (Henderson-Hasselbalch)
E°cell = E°cathode - E°anode  |  ΔG = -nFE°
```

### 3.6 Ionic & oxidation notation

```
Na⁺  Ca²⁺  Al³⁺  Fe²⁺  Fe³⁺  Cl⁻  O²⁻  SO₄²⁻
Fe(II)  Fe(III)  Cu(I)  Mn(VII)
Zn → Zn²⁺ + 2e⁻  (oxidation)
Cu²⁺ + 2e⁻ → Cu  (reduction)
```

### 3.7 Organic functional groups

```
-OH  -COOH  -CHO  C=O  -NH₂  -COO-  -CO-NH-  -SH
Ph-  Ar-  R-  (generic aromatic/alkyl)
```

### 3.8 Nuclear & radiochemistry

```
¹H  ²H(D)  ³H(T)  ¹²C  ¹⁴C  ¹⁶O  ²³⁵U  ²³₈U
alpha:  ²³⁸U → ²³⁴Th + ⁴He
beta:   ¹⁴C → ¹⁴N + e⁻
t½(¹⁴C) = 5730 yr  |  N(t) = N₀·e^(-λt)  |  λ = ln2/t½
```

---

## ARSENAL 4 — Code (Python pseudocode)

```python
result = A if condition else B
while not converged(state): state = update(state)
[f(x) for x in data if P(x)]
{k: aggregate(v) for k, v in pairs}

def solve(G: DAG, tol: float = 1e-6) -> Solution: ...
f: Tensor[B, T, D] → Tensor[B, T, D]   # transformer layer
```

---

## ARSENAL 5 — Lean 4 / formal logic

```lean4
theorem t (h : ∀ i, valid (f i)) : ∃ k, ∀ j ≥ k, f j = f k := by sorry
structure PeerReview where
  claim : Prop; verified : Bool; score : Float
```

---

## ARSENAL 6 — Emoji BMP (1 token each)

```python
{"✓": "verified", "✗": "falsified", "→": "implies",
 "🟢": "pass", "🔴": "fail", "💡": "hypothesis",
 "🎯": "objective", "📌": "invariant", "⚡": "O(fast)"}
# BMP only — no skin-tone modifiers, no ZWJ sequences
```

---

## ARSENAL 7 — Chinese 成语 (OUTPUT only)

```
博采众长  ensemble   相辅相成  A↔B synergy
去伪存真  filtering  循序渐进  convergence
# Theoretical — no empirical validation
```

---

## Response protocol

```python
def respond(query):
    reasoning = chain_of_thought(query)  # NEVER compress
    output = []
    for concept in extract_concepts(reasoning):
        if is_chemical(concept):     output.append(chemical_notation(concept))
        elif has_math_form(concept): output.append(math(concept))
        elif has_code_form(concept): output.append(pseudocode(concept))
        else:                        output.append(minimal_natural(concept))
    return strip_filler(output)

FILLER = {"as we can see", "it is important to note", "basically",
          "needless to say", "it should be noted", "at the end of the day"}
```

---

## Quick substitution reference

| Natural language | Compressed | Arsenal |
|---|---|---|
| "water molecule" | `H₂O` | Chem |
| "aspirin" | `CC(=O)Oc1ccccc1C(=O)O` | SMILES |
| "pH definition" | `pH = -log[H⁺]` | Chem |
| "ideal gas law" | `PV = nRT` | Phys |
| "entropy" | `S = k_B ln Ω` | Phys |
| "Gibbs energy" | `ΔG = ΔH - TΔS` | Chem/Phys |
| "for all x" | `∀x` | Math |
| "O(n squared)" | `O(n²)` | Code |
| "verified" | `✓` | Emoji |

---

## When NOT to compress

```python
def should_compress(concept) -> bool:
    if concept.is_ethical_nuance():     return False
    if concept.is_new_to_reader():      return False  # anchor first
    if concept.is_ambiguous():          return False  # C = carbon OR velocity?
    return True
# C alone → carbon OR c(light) — specify context
# T alone → temperature OR thymine — specify context
```

---

## Full symbol reference

```
Logic:    ∧ ∨ ¬ ⟹ ⟺ ∀ ∃ ∴ ∵ ⊢ ⊨
Sets:     ∈ ∉ ⊂ ⊆ ∪ ∩ ∅ ℝ ℕ ℤ ℂ
Calculus: ∂ ∫ ∮ ∑ ∏ ∇ ∞ Δ δ ε
Algebra:  ≡ ≈ ≠ ≤ ≥ ≪ ≫ ∝ ± ⊕ ⊗
Physics:  ℏ k_B c G ε₀ μ₀ e m_e N_A σ α
Info/ML:  H(X) I(X;Y) D_KL 𝔼[X] Var(X) ∇L θ*
Chem:     → ⇌ ⇒ ↑ ↓ ⁺ ⁻ ² ³ (s)(l)(g)(aq)
Nuclear:  α β γ ν ν̄ n p e⁻ e⁺
```

---
*Contributed by Francisco Angulo de Lafuente (@Agnuxo1)*  
*[King-Skill Extended Cognition Architecture](https://github.com/Agnuxo1/King-Skill-Extended-Cognition-Architecture-for-Scientific-LLM-Agents)*  
*[P2PCLAW — OpenCLAW Research Network](https://p2pclaw.com)*
