// Golog aspires to be an ISO Prolog interpreter.  It currently
// supports a small subset of the standard.  Any deviations from
// the standard are bugs.  Typical usage looks something like this:
//
//      m := NewMachine().Consult(`
//          father(john).
//          father(jacob).
//
//          mother(sue).
//
//          parent(X) :-
//              father(X).
//          parent(X) :-
//              mother(X).
//      `)
//      if m.CanProve(`father(john).`) {
//          fmt.Printf("john is a father\n")
//      }
//
//      solutions := m.ProveAll(`parent(X).`)
//      for _, solution := range solutions {
//          fmt.Printf("%s is a parent\n", solution.ByName_("X"))
//      }
//
// This sample highlights a few key aspects of using Golog.  To start,
// Golog data structures are immutable.  NewMachine() creates an empty
// Golog machine containing just the standard library.
// Consult() creates another new machine with some extra
// code loaded.  The original, empty machine is untouched.
// It's common to build a large Golog machine during init()
// and then add extra rules to it at runtime.
// Because Golog machines are immutable,
// multiple goroutines can access, run and "modify" machines in parallel.
// This design also opens possibilities for and-parallel and or-parallel
// execution.
//
// Most methods, like Consult(), can accept Prolog code in several forms.
// The example above shows Prolog as a string.  We could have used any
// io.Reader instead.
//
// Error handling is one oddity.  Golog methods follow Go convention by
// returning an error value to indicate that something went wrong.  However,
// in many cases the caller knows that an error is highly improbable and
// doesn't want extra code to deal with the common case.
// For most
// methods, Golog offers one with a trailing underscore, like ByName_(),
// which panics on error instead of returning an error value.
//
// See also:
//  * Golog's architecture: https://github.com/mndrix/golog/blob/master/doc/architecture.md
//  * Built in and foreign predicates: See func Builtin...
//  * Standard library: See golog/prelude package
package golog

import . "github.com/mndrix/golog/term"

import "github.com/mndrix/golog/read"
import "github.com/mndrix/golog/prelude"
import "github.com/mndrix/ps"

import "bufio"
import "bytes"
import "fmt"
import "os"

// NoBarriers error is returned when trying to access a cut barrier that
// doesn't exist.  See MostRecentCutBarrier
var NoBarriers = fmt.Errorf("There are no cut barriers")

// MachineDone error is returned when a Golog machine has been stepped
// as far forward as it can go.  It's unusual to need this variable.
var MachineDone = fmt.Errorf("Machine can't step any further")

// EmptyDisjunctions error is returned when trying to pop a disjunction
// off an empty disjunction stack.  This is mostly useful for those hacking
// on the interpreter.
var EmptyDisjunctions = fmt.Errorf("Disjunctions list is empty")

// EmptyConjunctions error is returned when trying to pop a conjunction
// off an empty conjunction stack.  This is mostly useful for those hacking
// on the interpreter.
var EmptyConjunctions = fmt.Errorf("Conjunctions list is empty")

// Golog users interact almost exclusively with a Machine value.
// Specifically, by calling one of the three methods Consult, CanProve and
// ProveAll.  All others methods are for those hacking on the interpreter or
// doing low-level operations in foreign predicates.
type Machine interface {
    // A Machine is an acceptable return value from a foreign predicate
    // definition.  In other words, a foreign predicate can perform low-level
    // manipulations on a Golog machine and return the result as the new
    // machine on which future execution occurs.  It's unlikely that you'll
    // need to do this.
    ForeignReturn

    // Temporary.  These will eventually become functions rather than methods.
    // All three accept Prolog terms as strings or as io.Reader objects from
    // which Prolog terms can be read.
    CanProve(interface{}) bool
    Consult(interface{}) Machine
    ProveAll(interface{}) []Bindings

    String() string

    // Bindings returns the machine's most current variable bindings.
    //
    // This method is typically only used by ChoicePoint implementations
    Bindings() Bindings

    // SetBindings returns a new machine like this one but with the given
    // bindings
    SetBindings(Bindings) Machine

    // PushConj returns a machine like this one but with an extra term
    // on front of the conjunction stack
    PushConj(Term) Machine

    // PopConj returns a machine with one less item on the conjunction stack
    // along with the term removed.  Returns err = EmptyConjunctions if there
    // are no more conjunctions on that stack
    PopConj() (Term, Machine, error)

    // ClearConj replaces the conjunction stack with an empty one
    ClearConjs() Machine

    // DemandCutBarrier makes sure the disjunction stack has a cut barrier
    // on top.  If not, one is pushed.
    // This marker can be used to locate which disjunctions came immediately
    // before the marker existed.
    DemandCutBarrier() Machine

    // MostRecentCutBarrier returns an opaque value which uniquely
    // identifies the most recent cut barrier on the disjunction stack.
    // Used with CutTo to remove several disjunctions at once.
    // Returns NoBarriers if there are no cut barriers on the disjunctions
    // stack.
    MostRecentCutBarrier() (int64, error)

    // CutTo removes all disjunctions stacked on top of a specific cut barrier.
    // It does not remove the cut barrier itself.
    // A barrier ID is obtained from MostRecentCutBarrier.
    CutTo(int64) Machine

    // PushDisj returns a machine like this one but with an extra ChoicePoint
    // on the disjunctions stack.
    PushDisj(ChoicePoint) Machine

    // PopDisj returns a machine with one fewer choice points on the
    // disjunction stack and the choice point that was removed.  Returns
    // err = EmptyDisjunctions if there are no more disjunctions on
    // that stack
    PopDisj() (ChoicePoint, Machine, error)

    // RegisterForeign registers Go functions to implement Golog predicates.
    // When Golog tries to prove a predicate with one of these predicate
    // indicators, it executes the given function instead.
    // Calling RegisterForeign with a predicate indicator that's already
    // been registered replaces the predicate implementation.
    RegisterForeign(map[string]ForeignPredicate) Machine

    // Step advances the machine one "step" (implementation dependent).
    // It produces a new machine which can take the next step.  It might
    // produce a proof by giving some variable bindings.  When the machine
    // has done as much work as it can do, it returns err=MachineDone
    Step() (Machine, Bindings, error)
}

// Golog allows Prolog predicates to be defined in Go.  The foreign predicate
// mechanism is implemented via functions whose type is ForeignPredicate.
type ForeignPredicate func(Machine, []Term) ForeignReturn

type machine struct {
    db      Database
    foreign ps.Map      // predicate indicator => ForeignPredicate
    env     Bindings
    disjs   ps.List     // of ChoicePoint
    conjs   ps.List     // of Term
}
func (*machine) IsaForeignReturn() {}

// NewMachine creates a new Golog machine.  This machine has the standard
// library already loaded and is typically the way one obtains
// a machine.
func NewMachine() Machine {
    return NewBlankMachine().
            Consult(prelude.Prelude).
            RegisterForeign(map[string]ForeignPredicate{
                "!/0" :         BuiltinCut,
                "$cut_to/1" :   BuiltinCutTo,
                ",/2" :         BuiltinComma,
                "->/2" :        BuiltinIfThen,
                ";/2" :         BuiltinSemicolon,
                "=/2" :         BuiltinUnify,
                "atom_codes/2": BuiltinAtomCodes2,
                "call/1" :      BuiltinCall,
                "call/2" :      BuiltinCall,
                "call/3" :      BuiltinCall,
                "call/4" :      BuiltinCall,
                "call/5" :      BuiltinCall,
                "call/6" :      BuiltinCall,
                "downcase_atom/2":  BuiltinDowncaseAtom2,
                "fail/0" :      BuiltinFail,
                "findall/3" :   BuiltinFindall3,
                "listing/0" :   BuiltinListing0,
                "msort/2" :     BuiltinMsort2,
                "printf/1":     BuiltinPrintf,
                "printf/2":     BuiltinPrintf,
                "printf/3":     BuiltinPrintf,
            })
}

// NewBlankMachine creates a new Golog machine without loading the
// standard library (prelude)
func NewBlankMachine() Machine {
    var m machine
    m.db = NewDatabase()
    m.foreign = ps.NewMap()
    m.env = NewBindings()
    m.disjs = ps.NewList()
    m.conjs = ps.NewList()
    return (&m).DemandCutBarrier()
}

func (m *machine) clone() *machine {
    m1 := *m
    return &m1
}

func (m *machine) Consult(text interface{}) Machine {
    terms := read.TermAll_(text)
    m1 := m.clone()
    for _, t := range terms {
        if IsDirective(t) {
            // ignore all directives, for now
            continue
        }
        m1.db = m1.db.Assertz(t)
    }
    return m1
}

func (m *machine) RegisterForeign(fs map[string]ForeignPredicate) Machine {
    m1 := m.clone()
    for indicator, f := range fs {
        m1.foreign = m1.foreign.Set(indicator, f)
    }
    return m1
}

func (m *machine) String() string {
    var buf bytes.Buffer
    fmt.Fprintf(&buf, "disjs:\n")
    m.disjs.ForEach( func (v ps.Any) {
        fmt.Fprintf(&buf, "  %s\n", v)
    })
    fmt.Fprintf(&buf, "conjs:\n")
    m.conjs.ForEach( func (v ps.Any) {
        fmt.Fprintf(&buf, "  %s\n", v)
    })
    fmt.Fprintf(&buf, "bindings: %s", m.env)
    return buf.String()
}

// CanProve returns true if goal can be proven from facts and clauses
// in the database
func (m *machine) CanProve(goal interface{}) bool {
    solutions := m.ProveAll(goal)
    return len(solutions) > 0
}

func (self *machine) ProveAll(goal interface{}) []Bindings {
    var answer Bindings
    var err error
    answers := make([]Bindings, 0)

    goalTerm := self.toGoal(goal)
    vars := Variables(goalTerm)  // preserve incoming human-readable names
    m := self.PushConj(goalTerm)
    for {
        m, answer, err = m.Step()
        if err == MachineDone {
            break
        }
        maybePanic(err)
        if answer != nil {
            answers = append(answers, answer.WithNames(vars))
        }
    }

    return answers
}

// advance the Golog machine one step closer to proving the goal at hand.
// at the end of each invocation, the top item on the conjunctions stack
// is the goal we should next try to prove.
func (self *machine) Step() (Machine, Bindings, error) {
    var m Machine = self
    var goal Term
    var err error
    var cp ChoicePoint

//  fmt.Printf("stepping...\n%s\n", self)
    if false {   // for debugging. commenting out needs import changes
        _, _ = bufio.NewReader(os.Stdin).ReadString('\n')
    }

    // find a goal other than true/0 to prove
    indicator := "true/0"
    for indicator == "true/0" {
        var mTmp Machine
        goal, mTmp, err = m.PopConj()
        if err == EmptyConjunctions {   // found an answer
            answer := m.Bindings()
//          fmt.Printf("  emitting answer %s\n", answer)
            m = m.PushConj(NewTerm("fail"))  // backtrack on next Step()
            return m, answer, nil
        }
        maybePanic(err)
        m = mTmp
        indicator = goal.Indicator()
    }

    // are we proving a foreign predicate?
    f, ok := m.(*machine).foreign.Lookup(indicator)
    if ok {     // foreign predicate
//      fmt.Printf("  running foreign predicate %s\n", indicator)
        args := m.(*machine).resolveAllArguments(goal)
        ret := f.(ForeignPredicate)(m, args)
        switch x := ret.(type) {
            case *foreignTrue:
                return m, nil, nil
            case *foreignFail:
                // do nothing. continue to iterate disjunctions below
            case *machine:
                return x, nil, nil
            case *foreignUnify:
                terms := []Term(*x)  // guaranteed even number of elements
                env := m.Bindings()
                for i := 0; i<len(terms); i+=2 {
                    env, err = terms[i].Unify(env, terms[i+1])
                    if err == CantUnify {
                        env = nil
                        break
                    }
                    maybePanic(err)
                }
                if env != nil {
                    return m.SetBindings(env), nil, nil
                }
        }
    } else {    // user-defined predicate, push all its disjunctions
//      args := m.(*machine).resolveAllArguments(goal)
//      fmt.Printf("  running user-defined predicate %s with \n", indicator, args)
        clauses, err := m.(*machine).db.Candidates(goal)
        maybePanic(err)
        m = m.DemandCutBarrier()
        for i:=len(clauses)-1; i>=0; i-- {
            clause := clauses[i]
            cp := NewHeadBodyChoicePoint(m, goal, clause)
            m = m.PushDisj(cp)
        }
    }

    // iterate disjunctions looking for one that succeeds
    for {
        cp, m, err = m.PopDisj()
        if err == EmptyDisjunctions {   // nothing left to do
            return nil, nil, MachineDone
        }
        maybePanic(err)

        // follow the next choice point
        mTmp, err := cp.Follow()
        if err == nil {
//          fmt.Printf("  followed CP %s\n", cp)
            return mTmp, nil, nil
        }
    }

    panic("Stepped a machine past the end")
}

func (m *machine) toGoal(thing interface{}) Term {
    switch x := thing.(type) {
        case Term:
            return x
        case string:
            return m.readTerm(x)
    }
    msg := fmt.Sprintf("Can't convert %#v to a term", thing)
    panic(msg)
}

func (m *machine) resolveAllArguments(goal Term) []Term {
    env := m.Bindings()
    args := goal.Arguments()
    resolved := make([]Term, len(args))
    for i, arg := range args {
        if IsVariable(arg) {
            a, err := env.Resolve(arg.(*Variable))
            if err == nil {
                resolved[i] = a
                continue
            }
        }
        resolved[i] = arg
    }

    return resolved
}

func (m *machine) readTerm(src interface{}) Term {
    return read.Term_(src)
}

func (m *machine) Bindings() Bindings {
    return m.env
}

func (m *machine) SetBindings(env Bindings) Machine {
    m1 := m.clone()
    m1.env = env
    return m1
}

func (m *machine) PushConj(t Term) Machine {
    // change all !/0 goals into '$cut_to'(RecentBarrierId) goals
    barrierID, err := m.MostRecentCutBarrier()
    if err == nil {
        t = resolveCuts(barrierID, t)
    }

    m1 := m.clone()
    m1.conjs = m.conjs.Cons(t)
    return m1
}

func (m *machine) PopConj() (Term, Machine, error) {
    if m.conjs.IsNil() {
        return nil, nil, EmptyConjunctions
    }

    t := m.conjs.Head().(Term)
    m1 := m.clone()
    m1.conjs = m.conjs.Tail()
    return t, m1, nil
}

func (m *machine) ClearConjs() Machine {
    m1 := m.clone()
    m1.conjs = ps.NewList()
    return m1
}

func (m *machine) PushDisj(cp ChoicePoint) Machine {
    m1 := m.clone()
    m1.disjs = m.disjs.Cons(cp)
    return m1
}

func (m *machine) PopDisj() (ChoicePoint, Machine, error) {
    if m.disjs.IsNil() {
        return nil, nil, EmptyDisjunctions
    }

    cp := m.disjs.Head().(ChoicePoint)
    m1 := m.clone()
    m1.disjs = m.disjs.Tail()
    return cp, m1, nil
}

func (m *machine) DemandCutBarrier() Machine {
    // is the top choice point already a cut barrier?
    if !m.disjs.IsNil() {
        topCP := m.disjs.Head().(ChoicePoint)
        _, ok := BarrierId(topCP)
        if ok { return m }
    }

    // nope, push a new cut barrier
    barrier := NewCutBarrier(m)
    return m.PushDisj(barrier)
}

func (m *machine) MostRecentCutBarrier() (int64, error) {
    ds := m.disjs
    for {
        if ds.IsNil() {
            return -1, NoBarriers
        }

        id, ok := BarrierId(ds.Head().(ChoicePoint))
        if ok {
            return id, nil
        }

        ds = ds.Tail()
    }
    panic("inconceivable!")
}

func (m *machine) CutTo(want int64) Machine {
    ds := m.disjs
    for {
        if ds.IsNil() {
            msg := fmt.Sprintf("No cut barrier with ID %d", want)
            panic(msg)
        }

        found, ok := BarrierId(ds.Head().(ChoicePoint))
        if ok && found == want {
            m1 := m.clone()
            m1.disjs = ds
            return m1
        }

        ds = ds.Tail()
    }
    panic("inconceivable!")
}

func resolveCuts(id int64, t Term) Term {
    switch t.Indicator() {
        case "!/0":
            return NewTerm("$cut_to", NewInt64(id))
        case ",/2", ";/2":
            args := t.Arguments()
            t0 := resolveCuts(id, args[0])
            t1 := resolveCuts(id, args[1])
            return NewTerm( t.Functor(), t0, t1 )
        case "->/2":
            args := t.Arguments()
            t0 := args[0]   // don't resolve cuts in Condition
            t1 := resolveCuts(id, args[1])
            return NewTerm( t.Functor(), t0, t1 )
    }

    // leave any other cuts unresolved
    return t
}

func maybePanic(err error) {
    if err != nil {
        panic(err)
    }
}
