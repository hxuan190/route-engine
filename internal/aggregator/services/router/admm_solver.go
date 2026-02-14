package router

import (
	"math"
	"math/big"

	"github.com/gagliardetto/solana-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
)

const (
	ADMMRho         = 1.0   // Penalty parameter
	ADMMMaxIter     = 15    // Max iterations (latency-bound)
	ADMMTol         = 0.005 // Convergence tolerance (0.5%)
	ADMMMinAlloc    = 0.05  // Minimum 5% allocation per path
	ADMMPruneThresh = 0.08  // Prune paths below 8% after optimization
)

// SplitAllocation represents a single path's allocation after ADMM optimization
type SplitAllocation struct {
	Path      []solana.PublicKey
	Hops      []domain.HopQuote
	Ratio     float64 // Allocation ratio (0.0 - 1.0)
	AmountIn  *big.Int
	AmountOut *big.Int
	TotalFee  *big.Int
	ImpactBps uint16
}

// ADMMSolver implements Alternating Direction Method of Multipliers
// for optimal N-path split allocation
type ADMMSolver struct {
	router  *Router
	rho     float64
	maxIter int
	tol     float64
}

// NewADMMSolver creates a new ADMM solver
func NewADMMSolver(router *Router) *ADMMSolver {
	return &ADMMSolver{
		router:  router,
		rho:     ADMMRho,
		maxIter: ADMMMaxIter,
		tol:     ADMMTol,
	}
}

// ADMMWorkspace holds pre-allocated buffers for zero-allocation ADMM solving
// Each goroutine or Router should maintain one workspace and reuse it across calls
type ADMMWorkspace struct {
	X          []float64 // Primal variable (allocation ratios)
	Z          []float64 // Consensus variable
	U          []float64 // Scaled dual variable
	V          []float64 // Temporary buffer for z-update
	SortBuf    []float64 // Buffer for projection sorting
	ProjectRes []float64 // Result buffer for projection (unused, kept for future)
}

// NewADMMWorkspace creates a new workspace with buffers sized for maxPaths
func NewADMMWorkspace(maxPaths int) *ADMMWorkspace {
	return &ADMMWorkspace{
		X:          make([]float64, maxPaths),
		Z:          make([]float64, maxPaths),
		U:          make([]float64, maxPaths),
		V:          make([]float64, maxPaths),
		SortBuf:    make([]float64, maxPaths),
		ProjectRes: make([]float64, maxPaths),
	}
}

// ensureCapacity checks and grows workspace if needed (Safety Net)
// Prevents silent failures when path count exceeds initial capacity
func (w *ADMMWorkspace) ensureCapacity(n int) {
	if len(w.X) < n {
		// Grow by 2x to avoid frequent reallocs in edge cases
		newCap := n * 2
		w.X = make([]float64, newCap)
		w.Z = make([]float64, newCap)
		w.U = make([]float64, newCap)
		w.V = make([]float64, newCap)
		w.SortBuf = make([]float64, newCap)
		w.ProjectRes = make([]float64, newCap)
	}
}

// Reset clears the workspace for reuse (O(1) operation via slice reslicing)
func (w *ADMMWorkspace) Reset() {
	// No need to zero out, we'll overwrite with new values
	// Just ensure slices are ready for reuse
}

// Solve finds the optimal split ratios for N paths using ADMM
// Maximizes total output (exactIn) or minimizes total input (exactOut)
// Uses workspace for zero-allocation (same pattern as SolveFast)
func (s *ADMMSolver) Solve(paths [][]solana.PublicKey, amount *big.Int, exactIn bool, ws *ADMMWorkspace) []SplitAllocation {
	n := len(paths)
	if n == 0 {
		return nil
	}

	// Safety: Auto-grow workspace if needed
	ws.ensureCapacity(n)

	// Use workspace buffers (slice to actual size needed)
	x := ws.X[:n]
	z := ws.Z[:n]
	u := ws.U[:n]
	v := ws.V[:n]
	sortBuf := ws.SortBuf[:n]

	// Initialize equal split
	initialRatio := 1.0 / float64(n)
	for i := 0; i < n; i++ {
		x[i] = initialRatio
		z[i] = initialRatio
		u[i] = 0.0
	}

	// Cache for path evaluations at different ratios
	// Key insight: we evaluate the "marginal return" of each path at its current allocation
	prevObjective := math.Inf(-1)

	for iter := 0; iter < s.maxIter; iter++ {
		// === x-update: optimize each path independently ===
		// For each path i, solve: minimize -f_i(x_i) + (rho/2)||x_i - z_i + u_i||^2
		// This is a 1D optimization per path, solved by evaluating a few candidate points
		for i := 0; i < n; i++ {
			target := z[i] - u[i]
			x[i] = s.optimizeSingleAllocation(paths[i], amount, target, exactIn)
		}

		// === z-update: projection onto simplex {z >= 0, sum(z) = 1} ===
		// Solve: minimize sum_i (rho/2)||x_i + u_i - z_i||^2 subject to simplex
		for i := 0; i < n; i++ {
			v[i] = x[i] + u[i]
		}

		// Use zero-alloc projection with workspace buffers
		projectOntoSimplexInto(v, z, sortBuf)

		// Enforce minimum allocation in-place
		enforceMinimumAllocationInPlace(z, ADMMMinAlloc)

		// === u-update: dual variable ===
		for i := 0; i < n; i++ {
			u[i] += x[i] - z[i]
		}

		// === Check convergence ===
		objective := s.evaluateObjective(paths, z, amount, exactIn)

		// Primal residual: ||x - z||^2 (squared Euclidean norm)
		primalResSq := 0.0
		for i := 0; i < n; i++ {
			diff := x[i] - z[i]
			primalResSq += diff * diff
		}

		// OPTIMIZATION: Compare squares to avoid expensive math.Sqrt()
		// Tolerance is 0.005, so tolSq = 0.000025
		tolSq := s.tol * s.tol
		if primalResSq < tolSq {
			break
		}

		// Check objective improvement stall
		if iter > 2 && objective > 0 && prevObjective > 0 {
			improvement := math.Abs(objective-prevObjective) / math.Abs(prevObjective)
			if improvement < s.tol/10 {
				break
			}
		}
		prevObjective = objective
	}

	// Build final allocations from z (consensus variable = final answer)
	return s.buildAllocations(paths, z, amount, exactIn)
}

// optimizeSingleAllocation finds optimal allocation for one path near the target ratio
// Uses 3-point evaluation around the target
func (s *ADMMSolver) optimizeSingleAllocation(path []solana.PublicKey, totalAmount *big.Int, target float64, exactIn bool) float64 {
	// Clamp target to valid range
	target = math.Max(0.0, math.Min(1.0, target))

	// Evaluate at target and two nearby points
	candidates := []float64{
		math.Max(0.0, target-0.05),
		target,
		math.Min(1.0, target+0.05),
	}

	bestRatio := target
	var bestOutput float64

	for i, ratio := range candidates {
		if ratio < 0.01 {
			continue // Skip near-zero allocations
		}

		amt := mulRatio(totalAmount, ratio)
		if amt.Sign() <= 0 {
			continue
		}

		quote, err := s.router.evaluateSinglePath(path, amt, exactIn)
		if err != nil || quote == nil {
			continue
		}

		var output float64
		if exactIn {
			outF := new(big.Float).SetInt(quote.AmountOut)
			output, _ = outF.Float64()
		} else {
			inF := new(big.Float).SetInt(quote.AmountIn)
			output, _ = inF.Float64()
			output = -output // Negate for minimization
		}

		// Add proximity penalty: (rho/2) * (ratio - target)^2
		penalty := (s.rho / 2.0) * (ratio - target) * (ratio - target)
		score := output - penalty

		if i == 0 || score > bestOutput {
			bestOutput = score
			bestRatio = ratio
		}
	}

	return bestRatio
}

// evaluateObjective computes the total objective value for a given allocation
func (s *ADMMSolver) evaluateObjective(paths [][]solana.PublicKey, ratios []float64, amount *big.Int, exactIn bool) float64 {
	total := 0.0
	for i, path := range paths {
		if ratios[i] < 0.01 {
			continue
		}

		amt := mulRatio(amount, ratios[i])
		if amt.Sign() <= 0 {
			continue
		}

		quote, err := s.router.evaluateSinglePath(path, amt, exactIn)
		if err != nil || quote == nil {
			continue
		}

		if exactIn {
			f := new(big.Float).SetInt(quote.AmountOut)
			v, _ := f.Float64()
			total += v
		} else {
			f := new(big.Float).SetInt(quote.AmountIn)
			v, _ := f.Float64()
			total -= v // Minimize input
		}
	}
	return total
}

// buildAllocations converts final ratios into SplitAllocation structs with full evaluation
func (s *ADMMSolver) buildAllocations(paths [][]solana.PublicKey, ratios []float64, amount *big.Int, exactIn bool) []SplitAllocation {
	allocs := make([]SplitAllocation, 0, len(paths))

	for i, path := range paths {
		if ratios[i] < ADMMPruneThresh {
			continue // Prune negligible allocations
		}

		amt := mulRatio(amount, ratios[i])
		if amt.Sign() <= 0 {
			continue
		}

		quote, err := s.router.evaluateSinglePath(path, amt, exactIn)
		if err != nil || quote == nil {
			continue
		}

		allocs = append(allocs, SplitAllocation{
			Path:      path,
			Hops:      quote.Hops,
			Ratio:     ratios[i],
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			TotalFee:  quote.TotalFee,
			ImpactBps: quote.PriceImpactBps,
		})
	}

	return allocs
}

// projectOntoSimplex projects a vector onto the probability simplex
// {x >= 0, sum(x) = 1} using the algorithm by Duchi et al. (2008)
func projectOntoSimplex(v []float64) []float64 {
	n := len(v)
	if n == 0 {
		return v
	}

	// Sort in descending order
	u := make([]float64, n)
	copy(u, v)
	// Simple insertion sort (n is small, <= MaxSplitPaths)
	for i := 1; i < n; i++ {
		key := u[i]
		j := i - 1
		for j >= 0 && u[j] < key {
			u[j+1] = u[j]
			j--
		}
		u[j+1] = key
	}

	// Find rho (the number of positive elements in the projection)
	cumSum := 0.0
	rho := 0
	for j := 0; j < n; j++ {
		cumSum += u[j]
		if u[j] > (cumSum-1.0)/float64(j+1) {
			rho = j + 1
		}
	}

	// Compute threshold
	cumSum = 0.0
	for j := 0; j < rho; j++ {
		cumSum += u[j]
	}
	theta := (cumSum - 1.0) / float64(rho)

	// Project
	result := make([]float64, n)
	for i := 0; i < n; i++ {
		result[i] = math.Max(v[i]-theta, 0.0)
	}

	return result
}

// enforceMinimumAllocation ensures each active path has at least minAlloc
// and redistributes from larger allocations
func enforceMinimumAllocation(ratios []float64, minAlloc float64) []float64 {
	n := len(ratios)
	result := make([]float64, n)
	copy(result, ratios)

	// Count active paths (above threshold)
	activeCount := 0
	for _, r := range result {
		if r > 0.001 {
			activeCount++
		}
	}

	if activeCount == 0 {
		return result
	}

	// If minimum allocation * activeCount > 1.0, reduce minimum
	effectiveMin := minAlloc
	if float64(activeCount)*effectiveMin > 1.0 {
		effectiveMin = 1.0 / float64(activeCount)
	}

	// Enforce minimum and track deficit/surplus
	deficit := 0.0
	surplusTotal := 0.0

	for i := range result {
		if result[i] > 0.001 && result[i] < effectiveMin {
			deficit += effectiveMin - result[i]
			result[i] = effectiveMin
		} else if result[i] > effectiveMin {
			surplusTotal += result[i] - effectiveMin
		}
	}

	// Redistribute deficit proportionally from surplus paths
	if deficit > 0 && surplusTotal > 0 {
		for i := range result {
			if result[i] > effectiveMin {
				surplus := result[i] - effectiveMin
				reduction := deficit * (surplus / surplusTotal)
				result[i] -= reduction
			}
		}
	}

	// Normalize to ensure sum = 1.0
	sum := 0.0
	for _, r := range result {
		sum += r
	}
	if sum > 0 {
		for i := range result {
			result[i] /= sum
		}
	}

	return result
}

// projectOntoSimplexInto projects a vector onto the probability simplex in-place
// {x >= 0, sum(x) = 1} using the algorithm by Duchi et al. (2008)
// Input: v (vector to project), out (result buffer), sortBuf (sorting buffer)
// Output: out contains the projection result
func projectOntoSimplexInto(v []float64, out []float64, sortBuf []float64) {
	n := len(v)
	if n == 0 {
		return
	}

	// Copy v into sortBuf for sorting
	copy(sortBuf, v)

	// Sort in descending order (insertion sort, n is small)
	for i := 1; i < n; i++ {
		key := sortBuf[i]
		j := i - 1
		for j >= 0 && sortBuf[j] < key {
			sortBuf[j+1] = sortBuf[j]
			j--
		}
		sortBuf[j+1] = key
	}

	// Find rho (the number of positive elements in the projection)
	cumSum := 0.0
	rho := 0
	for j := 0; j < n; j++ {
		cumSum += sortBuf[j]
		if sortBuf[j] > (cumSum-1.0)/float64(j+1) {
			rho = j + 1
		}
	}

	// Compute threshold
	cumSum = 0.0
	for j := 0; j < rho; j++ {
		cumSum += sortBuf[j]
	}
	theta := (cumSum - 1.0) / float64(rho)

	// Project into out (inline max for performance)
	for i := 0; i < n; i++ {
		val := v[i] - theta
		if val < 0.0 {
			val = 0.0
		}
		out[i] = val
	}
}

// enforceMinimumAllocationInPlace ensures each active path has at least minAlloc
// and redistributes from larger allocations (modifies ratios in-place)
func enforceMinimumAllocationInPlace(ratios []float64, minAlloc float64) {
	// Count active paths (above threshold)
	activeCount := 0
	for _, r := range ratios {
		if r > 0.001 {
			activeCount++
		}
	}

	if activeCount == 0 {
		return
	}

	// If minimum allocation * activeCount > 1.0, reduce minimum
	effectiveMin := minAlloc
	if float64(activeCount)*effectiveMin > 1.0 {
		effectiveMin = 1.0 / float64(activeCount)
	}

	// Enforce minimum and track deficit/surplus
	deficit := 0.0
	surplusTotal := 0.0

	for i := range ratios {
		if ratios[i] > 0.001 && ratios[i] < effectiveMin {
			deficit += effectiveMin - ratios[i]
			ratios[i] = effectiveMin
		} else if ratios[i] > effectiveMin {
			surplusTotal += ratios[i] - effectiveMin
		}
	}

	// Redistribute deficit proportionally from surplus paths
	if deficit > 0 && surplusTotal > 0 {
		for i := range ratios {
			if ratios[i] > effectiveMin {
				surplus := ratios[i] - effectiveMin
				reduction := deficit * (surplus / surplusTotal)
				ratios[i] -= reduction
			}
		}
	}

	// Normalize to ensure sum = 1.0
	sum := 0.0
	for _, r := range ratios {
		sum += r
	}
	if sum > 0 {
		for i := range ratios {
			ratios[i] /= sum
		}
	}
}

// SolveFast finds the optimal split ratios for N paths using ADMM with uint64 (zero-allocation)
// Workspace must be provided by caller and should be sized >= len(paths)
func (s *ADMMSolver) SolveFast(paths [][]solana.PublicKey, amount uint64, exactIn bool, ws *ADMMWorkspace) []FastSplitAllocation {
	n := len(paths)
	if n == 0 {
		return nil
	}

	// Safety: Auto-grow instead of returning nil
	ws.ensureCapacity(n)

	// Use workspace buffers (slice to actual size needed)
	x := ws.X[:n]
	z := ws.Z[:n]
	u := ws.U[:n]
	v := ws.V[:n]
	sortBuf := ws.SortBuf[:n]

	// Initialize equal split
	initialRatio := 1.0 / float64(n)
	for i := 0; i < n; i++ {
		x[i] = initialRatio
		z[i] = initialRatio
		u[i] = 0.0
	}

	prevObjective := math.Inf(-1)

	for iter := 0; iter < s.maxIter; iter++ {
		// x-update: optimize each path independently
		for i := 0; i < n; i++ {
			target := z[i] - u[i]
			x[i] = s.optimizeSingleAllocationFast(paths[i], amount, target, exactIn)
		}

		// z-update: projection onto simplex
		for i := 0; i < n; i++ {
			v[i] = x[i] + u[i]
		}

		// Zero-alloc projection using workspace buffers
		projectOntoSimplexInto(v, z, sortBuf)

		// Enforce minimum allocation in-place
		enforceMinimumAllocationInPlace(z, ADMMMinAlloc)

		// u-update: dual variable
		for i := 0; i < n; i++ {
			u[i] += x[i] - z[i]
		}

		// Check convergence
		objective := s.evaluateObjectiveFast(paths, z, amount, exactIn)

		// Primal residual: ||x - z||^2 (squared Euclidean norm)
		primalResSq := 0.0
		for i := 0; i < n; i++ {
			diff := x[i] - z[i]
			primalResSq += diff * diff
		}

		// OPTIMIZATION: Compare squares to avoid expensive math.Sqrt()
		tolSq := s.tol * s.tol
		if primalResSq < tolSq {
			break
		}

		if iter > 2 && objective > 0 && prevObjective > 0 {
			improvement := math.Abs(objective-prevObjective) / math.Abs(prevObjective)
			if improvement < s.tol/10 {
				break
			}
		}
		prevObjective = objective
	}

	return s.buildAllocationsFast(paths, z, amount, exactIn)
}

// optimizeSingleAllocationFast finds optimal allocation for one path using uint64
// Uses inline min/max and unrolled loop to avoid function call overhead
func (s *ADMMSolver) optimizeSingleAllocationFast(path []solana.PublicKey, totalAmount uint64, target float64, exactIn bool) float64 {
	// Inline clamp: math.Max(0.0, math.Min(1.0, target))
	if target < 0.0 {
		target = 0.0
	}
	if target > 1.0 {
		target = 1.0
	}

	// UNROLL LOOP: Evaluate 3 candidates without allocating slice
	// Candidate 1: Lower bound (target - 0.05)
	cand1 := target - 0.05
	if cand1 < 0.0 {
		cand1 = 0.0 // Inline Max(0, ...)
	}
	bestRatio := cand1
	bestScore := s.scoreRatioFast(path, totalAmount, cand1, target, exactIn)

	// Candidate 2: Target
	score2 := s.scoreRatioFast(path, totalAmount, target, target, exactIn)
	if score2 > bestScore {
		bestRatio = target
		bestScore = score2
	}

	// Candidate 3: Upper bound (target + 0.05)
	cand3 := target + 0.05
	if cand3 > 1.0 {
		cand3 = 1.0 // Inline Min(1, ...)
	}
	score3 := s.scoreRatioFast(path, totalAmount, cand3, target, exactIn)
	if score3 > bestScore {
		bestRatio = cand3
	}

	return bestRatio
}

// scoreRatioFast computes the score for a given ratio (helper for optimization)
func (s *ADMMSolver) scoreRatioFast(path []solana.PublicKey, totalAmount uint64, ratio, target float64, exactIn bool) float64 {
	if ratio < 0.01 {
		return -1e9 // Penalty for near-zero allocations
	}

	amt := mulRatioU64(totalAmount, ratio)
	if amt == 0 {
		return -1e9
	}

	quote, err := s.router.evaluateSinglePathFast(path, amt, exactIn)
	if err != nil || quote == nil {
		return -1e9
	}

	var output float64
	if exactIn {
		output = float64(quote.AmountOut)
	} else {
		output = -float64(quote.AmountIn)
	}

	// Add proximity penalty: (rho/2) * (ratio - target)^2
	penalty := (s.rho / 2.0) * (ratio - target) * (ratio - target)
	return output - penalty
}

// evaluateObjectiveFast computes the total objective value using uint64
func (s *ADMMSolver) evaluateObjectiveFast(paths [][]solana.PublicKey, ratios []float64, amount uint64, exactIn bool) float64 {
	total := 0.0
	for i, path := range paths {
		if ratios[i] < 0.01 {
			continue
		}

		amt := mulRatioU64(amount, ratios[i])
		if amt == 0 {
			continue
		}

		quote, err := s.router.evaluateSinglePathFast(path, amt, exactIn)
		if err != nil || quote == nil {
			continue
		}

		if exactIn {
			total += float64(quote.AmountOut)
		} else {
			total -= float64(quote.AmountIn)
		}
	}
	return total
}

// buildAllocationsFast converts final ratios into FastSplitAllocation structs
func (s *ADMMSolver) buildAllocationsFast(paths [][]solana.PublicKey, ratios []float64, amount uint64, exactIn bool) []FastSplitAllocation {
	allocs := make([]FastSplitAllocation, 0, len(paths))

	for i, path := range paths {
		if ratios[i] < ADMMPruneThresh {
			continue
		}

		amt := mulRatioU64(amount, ratios[i])
		if amt == 0 {
			continue
		}

		quote, err := s.router.evaluateSinglePathFast(path, amt, exactIn)
		if err != nil || quote == nil {
			continue
		}

		allocs = append(allocs, FastSplitAllocation{
			Path:      path,
			Hops:      quote.Hops,
			Ratio:     ratios[i],
			AmountIn:  quote.AmountIn,
			AmountOut: quote.AmountOut,
			TotalFee:  quote.TotalFee,
			ImpactBps: quote.PriceImpactBps,
		})
	}

	return allocs
}

// FastAllocationsToSplitResult converts fast ADMM allocations to FastSplitResult
func FastAllocationsToSplitResult(allocs []FastSplitAllocation) *FastSplitResult {
	if len(allocs) == 0 {
		return nil
	}

	splits := make([]FastSplitPath, len(allocs))
	var totalIn, totalOut, totalFee uint64
	weightedImpact := uint32(0)

	totalRatio := 0.0
	for _, a := range allocs {
		totalRatio += a.Ratio
	}

	percentSum := uint8(0)
	for i, a := range allocs {
		pct := uint8(math.Round(a.Ratio / totalRatio * 100))
		if pct == 0 && a.Ratio > 0 {
			pct = 1
		}
		percentSum += pct

		splits[i] = FastSplitPath{
			Path:      a.Path,
			Hops:      a.Hops,
			Percent:   pct,
			AmountIn:  a.AmountIn,
			AmountOut: a.AmountOut,
			FeeAmount: a.TotalFee,
			ImpactBps: a.ImpactBps,
		}

		totalIn += a.AmountIn
		totalOut += a.AmountOut
		totalFee += a.TotalFee
		weightedImpact += uint32(a.ImpactBps) * uint32(pct)
	}

	if percentSum != 100 && len(splits) > 0 {
		diff := int(100) - int(percentSum)
		maxIdx := 0
		for i := 1; i < len(splits); i++ {
			if splits[i].Percent > splits[maxIdx].Percent {
				maxIdx = i
			}
		}
		newPct := int(splits[maxIdx].Percent) + diff
		if newPct > 0 {
			splits[maxIdx].Percent = uint8(newPct)
		}
	}

	avgImpact := uint16(0)
	if percentSum > 0 {
		avgImpact = uint16(weightedImpact / uint32(percentSum))
	}

	return &FastSplitResult{
		Splits:         splits,
		TotalAmountIn:  totalIn,
		TotalAmountOut: totalOut,
		TotalFee:       totalFee,
		AvgImpactBps:   avgImpact,
	}
}

// AllocationsToSplitResult converts ADMM allocations to a SplitResult
func AllocationsToSplitResult(allocs []SplitAllocation) *SplitResult {
	if len(allocs) == 0 {
		return nil
	}

	splits := make([]SplitPath, len(allocs))
	totalIn := big.NewInt(0)
	totalOut := big.NewInt(0)
	totalFee := big.NewInt(0)
	weightedImpact := uint32(0)

	// Normalize ratios to percentages (must sum to 100)
	totalRatio := 0.0
	for _, a := range allocs {
		totalRatio += a.Ratio
	}

	percentSum := uint8(0)
	for i, a := range allocs {
		pct := uint8(math.Round(a.Ratio / totalRatio * 100))
		if pct == 0 && a.Ratio > 0 {
			pct = 1
		}
		percentSum += pct

		splits[i] = SplitPath{
			Path:      a.Path,
			Hops:      a.Hops,
			Percent:   pct,
			AmountIn:  a.AmountIn,
			AmountOut: a.AmountOut,
			FeeAmount: a.TotalFee,
			ImpactBps: a.ImpactBps,
		}

		totalIn.Add(totalIn, a.AmountIn)
		totalOut.Add(totalOut, a.AmountOut)
		if a.TotalFee != nil {
			totalFee.Add(totalFee, a.TotalFee)
		}
		weightedImpact += uint32(a.ImpactBps) * uint32(pct)
	}

	// Fix rounding: adjust largest allocation so total = 100
	if percentSum != 100 && len(splits) > 0 {
		diff := int(100) - int(percentSum)
		// Find largest allocation to adjust
		maxIdx := 0
		for i := 1; i < len(splits); i++ {
			if splits[i].Percent > splits[maxIdx].Percent {
				maxIdx = i
			}
		}
		newPct := int(splits[maxIdx].Percent) + diff
		if newPct > 0 {
			splits[maxIdx].Percent = uint8(newPct)
		}
	}

	avgImpact := uint16(0)
	if percentSum > 0 {
		avgImpact = uint16(weightedImpact / uint32(percentSum))
	}

	return &SplitResult{
		Splits:         splits,
		TotalAmountIn:  totalIn,
		TotalAmountOut: totalOut,
		TotalFee:       totalFee,
		AvgImpactBps:   avgImpact,
	}
}
