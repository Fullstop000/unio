import asyncio

import unio


async def main() -> None:
    async with unio.Agent(unio.Codex, cwd=".") as agent:
        session = agent.new_session()
        stream = await session.stream("Inspect every file in this repository")

        async def stop_soon() -> None:
            await asyncio.sleep(2)
            await session.interrupt()

        stopper = asyncio.create_task(stop_soon())
        result = await stream.result()
        await stopper
        print(f"interrupted={result.interrupted} partial_text={result.text!r}")


if __name__ == "__main__":
    asyncio.run(main())
