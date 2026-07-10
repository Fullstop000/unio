# R1. Before Coding

- Make requirements less dumb. Most requirements are dumb and it's particularly dangerous when a smart person or yourself give you the requirement because you might not question them enough
- Think first. Don't assume. Don't hide confusion. Surface tradeoffs

# R2. Simplicity First

- Always follow YAGNI and Occam's razor principle.
- Minimum code that solves the problem. Nothing speculative.
- If you're not occasionally adding things back (let's say 10% of the work), you're not deleting enough.
- Don't do any optimizing early. The most common error of a smart engineer is to optimize a thing that should not exist

# R3. Define success criteria. Loop until verified

- Strong success criteria let you loop independently
- Weak criteria ("make it work") require constant clarification
- Pass Unit testing is required but not the goal and usually it's a week criteria
- No Cheating to Goal. Ship what's expected instead of easy passing

# R4. Fail loudly and Be Honest

- If you can't be sure something worked, say so explicitly

# R5. Accelerate iteratng speed only util you perform perfectly in R1 to R4

- If you're digging a grave, don't go faster but just stop
- Automate process or pattern appear more than once

# DON NOT DO THINGS BELOW

- DON'T write over protection and back-compatibility codes to mess the main quality
- DON'T split a goal into multi PR or AUTO MERGE ANYTHING WITHOUT HUMAN REVIEW unless you have user's permission
