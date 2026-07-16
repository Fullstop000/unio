import asyncio

import unio


async def main() -> None:
    async with unio.Agent(unio.Claude, cwd=".") as agent:
        stream = await agent.new_session().stream(unio.UserMessage("Review this repository"))
        async for event in stream:
            if event.kind in {unio.EventKind.TEXT, unio.EventKind.THINKING}:
                print(event.text, end="", flush=True)
            elif event.kind is unio.EventKind.TOOL_CALL:
                print(f"\ntool={event.tool} input={event.tool_input!r}")
        await stream.result()


if __name__ == "__main__":
    asyncio.run(main())
